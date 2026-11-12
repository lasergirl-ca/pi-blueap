// Package web is the management web UI and API served by palm-gwd (PLAN.md
// section 8.12, docs/WEB-UI.md). It has three kinds of listener: the LAN side,
// the Bluetooth side on the gateway address (TLS only), and the port-80 "you
// are connected" page on the gateway address. Every action goes through the
// control socket's handlers in-process (ctl.Server.Call), so the UI offers
// what palm-gwctl offers.
package web

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Backend runs control-socket commands; *ctl.Server is one.
type Backend interface {
	Call(ctx context.Context, req ctl.Request, emit func(line json.RawMessage) error) error
}

// Options configure the server. Config is read on every login, so a new
// password (palm-gw web set-password, then reload) takes effect at once.
type Options struct {
	Config        func() *config.Config
	Backend       Backend
	TLS           *tls.Config // nil: [web].tls = "off" (LAN only, D-039)
	Fingerprint   string      // the certificate's SHA-256, shown on the login page
	ConnectedPort int         // the gateway's plain page; 0 = none
	Version       string
	Log           *slog.Logger
	Addrs         func() ([]IfAddr, error) // default SystemAddrs
	Now           func() time.Time         // default time.Now
	StateDir      string                   // where captures/ and bundles/ are, for downloads

	// Filled from Config by New.
	Listen        []string
	Gateway       string
	Bridge        string
	BluetoothSide bool
	AllowCIDRs    []netip.Prefix
	SessionTTL    time.Duration
}

// Server is the web UI.
type Server struct {
	o        Options
	log      *slog.Logger
	tmpl     *template.Template
	sessions *sessions
	limiter  *limiter
	ctx      context.Context // the server's lifetime: owns pairing windows opened here
	pair     pairRunner
}

// New prepares the server from the configuration at start; listeners follow
// address changes, the rest of [web] needs a restart (D-030).
func New(o Options) (*Server, error) {
	if o.Config == nil || o.Backend == nil {
		return nil, errors.New("web: Config and Backend are required")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Addrs == nil {
		o.Addrs = SystemAddrs
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	cfg := o.Config()
	o.Listen = cfg.Web.Listen
	o.Gateway = cfg.Network.Gateway
	o.Bridge = cfg.PAN.Bridge
	o.BluetoothSide = cfg.Web.BluetoothSide
	o.SessionTTL = time.Duration(cfg.Web.SessionHours) * time.Hour
	for _, c := range cfg.Web.AllowCIDRs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			a, err2 := netip.ParseAddr(c)
			if err2 != nil {
				return nil, fmt.Errorf("web: allow_cidrs: %w", err)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		o.AllowCIDRs = append(o.AllowCIDRs, p)
	}
	t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: templates: %w", err)
	}
	return &Server{
		o:        o,
		log:      o.Log.With("component", "web"),
		tmpl:     t,
		sessions: newSessions(o.Now),
		limiter:  newLimiter(o.Now),
		ctx:      context.Background(),
	}, nil
}

// Run keeps the listeners bound until ctx is done.
func (s *Server) Run(ctx context.Context) {
	s.ctx = ctx
	s.keepListeners(ctx, 5*time.Second)
}

type ctxKey int

const sessionKey ctxKey = 0

func sessionOf(r *http.Request) *session {
	se, _ := r.Context().Value(sessionKey).(*session)
	return se
}

// Handler is the route set for one listener. The side and whether the
// connection is TLS are fixed by the listener, never by the request.
func (s *Server) Handler(side Side, isTLS bool) http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	if side == SideConnected {
		mux.HandleFunc("GET /{$}", s.connectedPage)
		return headers(mux)
	}
	mux.HandleFunc("GET /login", s.loginPage(side))
	mux.HandleFunc("POST /login", s.login(side, isTLS))
	mux.Handle("POST /logout", s.auth(side, s.logout(isTLS)))
	s.routes(mux, side)
	s.settingsRoutes(mux, side)
	s.internetRoutes(mux, side)
	s.diagRoutes(mux, side)
	s.apiRoutes(mux, side)
	var h http.Handler = mux
	if side == SideLAN && len(s.o.AllowCIDRs) > 0 {
		h = s.allowCIDRs(h)
	}
	return headers(h)
}

// headers sets the security headers every reply carries.
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// allowCIDRs refuses LAN sources outside [web].allow_cidrs.
func (s *Server) allowCIDRs(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := netip.ParseAddr(clientIP(r))
		if err == nil {
			a = a.Unmap()
			for _, p := range s.o.AllowCIDRs {
				if p.Contains(a) {
					h.ServeHTTP(w, r)
					return
				}
			}
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// auth requires a session from this side. A POST must carry the session's
// CSRF token, and a viewer may POST only to log out.
func (s *Server) auth(side Side, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var se *session
		if c, err := r.Cookie(cookieName); err == nil {
			se = s.sessions.get(c.Value, side)
		}
		if se != nil && se.api {
			se = nil // an API token is not a cookie (D-058)
		}
		if se == nil {
			if r.Header.Get("HX-Request") == "true" {
				// An htmx refresh would follow a redirect and swap the whole
				// login page into the part it refreshes; HX-Redirect makes
				// the browser load the login page instead.
				w.Header().Set("HX-Redirect", "/login")
				http.Error(w, "not logged in", http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				http.Error(w, "not logged in", http.StatusUnauthorized)
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			tok := r.Header.Get(csrfHeader)
			if tok == "" {
				tok = r.PostFormValue(csrfField)
			}
			if subtleEq(tok, se.csrf) != 1 {
				s.log.Warn("web: bad CSRF token", "event", "web_csrf_rejected", "side", side, "ip", clientIP(r), "path", r.URL.Path)
				http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
				return
			}
			if se.role != RoleAdmin && r.URL.Path != "/logout" {
				http.Error(w, "read-only account", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, se)))
	})
}

// accounts are the logins the current configuration allows.
func (s *Server) accounts() []account {
	w := s.o.Config().Web
	out := []account{{user: w.Username, hash: w.PasswordHash, role: RoleAdmin}}
	if w.ViewerUsername != "" {
		out = append(out, account{user: w.ViewerUsername, hash: w.ViewerPasswordHash, role: RoleViewer})
	}
	return out
}

func (s *Server) loginPage(side Side) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, http.StatusOK, "login", s.loginData(side, ""))
	}
}

func (s *Server) loginData(side Side, msg string) map[string]any {
	set := false
	for _, a := range s.accounts() {
		set = set || a.hash != ""
	}
	return map[string]any{
		"Title":       "Log in",
		"Error":       msg,
		"NoPassword":  !set,
		"Fingerprint": s.o.Fingerprint,
		"Side":        side,
		"Gateway":     s.o.Config().Bluetooth.Name,
	}
}

func (s *Server) login(side Side, isTLS bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if blocked, wait := s.limiter.blocked(side, ip); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			s.render(w, r, http.StatusTooManyRequests, "login", s.loginData(side, "Too many failed logins. Wait a minute and try again."))
			return
		}
		user := r.PostFormValue("username")
		role := check(s.accounts(), user, r.PostFormValue("password"))
		if role == "" {
			n := s.limiter.failed(side, ip)
			s.log.Warn("web login failed", "event", "web_login_failed", "side", side, "ip", ip, "failures", n)
			s.render(w, r, http.StatusUnauthorized, "login", s.loginData(side, "Wrong username or password."))
			return
		}
		se := s.sessions.create(user, role, side, s.o.SessionTTL)
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: se.token, Path: "/",
			Expires: se.expires, HttpOnly: true, Secure: isTLS, SameSite: http.SameSiteStrictMode,
		})
		s.log.Info("web login", "event", "web_login", "side", side, "ip", ip, "user", user, "role", role)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (s *Server) logout(isTLS bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		se := sessionOf(r)
		s.sessions.drop(se.token)
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isTLS, SameSite: http.SameSiteStrictMode})
		s.log.Info("web logout", "event", "web_logout", "side", se.side, "ip", clientIP(r), "user", se.user)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// call runs one control command and decodes its single reply into out.
func (s *Server) call(ctx context.Context, req ctl.Request, out any) error {
	got := false
	return s.o.Backend.Call(ctx, req, func(line json.RawMessage) error {
		if got {
			return nil
		}
		got = true
		return json.Unmarshal(line, out)
	})
}

// page is the data every logged-in page gets.
func (s *Server) page(r *http.Request, title string) map[string]any {
	se := sessionOf(r)
	return map[string]any{
		"Title":   title,
		"User":    se.user,
		"Role":    se.role,
		"CSRF":    se.csrf,
		"Side":    se.side,
		"Version": s.o.Version,
		"Gateway": s.o.Config().Bluetooth.Name,
	}
}

func (s *Server) connectedPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":   "You are connected",
		"IP":      clientIP(r),
		"Gateway": s.o.Gateway,
		"Name":    s.o.Config().Bluetooth.Name,
	}
	if s.o.BluetoothSide && s.o.TLS != nil && len(s.o.Listen) > 0 {
		if _, port, err := splitPort(s.o.Listen[0]); err == nil {
			data["AdminURL"] = fmt.Sprintf("https://%s:%s/", s.o.Gateway, port)
		}
	}
	s.render(w, r, http.StatusOK, "connected", data)
}

func (s *Server) render(w http.ResponseWriter, _ *http.Request, code int, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Warn("web: template", "name", name, "err", err)
	}
}
