package web

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Side says which side of the gateway a listener serves (docs/WEB-UI.md
// section 1). It comes only from the listener that accepted the connection,
// never from anything the client sends.
type Side string

const (
	SideLAN       Side = "lan"       // the home LAN: [web].listen on non-Palm interfaces
	SideBluetooth Side = "bluetooth" // the gateway address, TLS only (D-026, D-039)
	SideConnected Side = "connected" // the gateway address, port 80: the "you are connected" page
)

// IfAddr is one IPv4 address on one interface.
type IfAddr struct {
	Iface string
	IP    net.IP
}

// SystemAddrs lists the host's IPv4 addresses.
func SystemAddrs() ([]IfAddr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []IfAddr
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, IfAddr{Iface: ifc.Name, IP: n.IP.To4()})
			}
		}
	}
	return out, nil
}

// palmIface reports whether an interface is on the Palm side: the PAN bridge,
// a bnep port or a PPP link (PLAN.md section 8.12, "Binding and exposure").
func palmIface(name, bridge string) bool {
	return name == bridge || strings.HasPrefix(name, "bnep") || strings.HasPrefix(name, "ppp")
}

// bindSpec is one address the server should be listening on.
type bindSpec struct {
	Addr string // ip:port
	Side Side
	TLS  bool
}

// wanted works out the listeners for the current addresses: [web].listen on
// every non-Palm interface for the LAN side; the gateway address on the
// listen port (TLS) and on port 80 once the bridge holds it.
func wanted(o Options, addrs []IfAddr) []bindSpec {
	var out []bindSpec
	seen := map[string]bool{}
	add := func(b bindSpec) {
		if !seen[b.Addr] {
			seen[b.Addr] = true
			out = append(out, b)
		}
	}
	lanTLS := o.TLS != nil
	btPort := ""
	gwUp := false
	for _, a := range addrs {
		if a.IP.String() == o.Gateway {
			gwUp = true
		}
	}
	for _, l := range o.Listen {
		host, port, err := net.SplitHostPort(l)
		if err != nil {
			continue
		}
		if btPort == "" {
			btPort = port
		}
		for _, a := range addrs {
			if palmIface(a.Iface, o.Bridge) || a.IP.String() == o.Gateway {
				continue
			}
			if host == "" || host == "0.0.0.0" || host == a.IP.String() {
				add(bindSpec{Addr: net.JoinHostPort(a.IP.String(), port), Side: SideLAN, TLS: lanTLS})
			}
		}
	}
	if gwUp && o.Gateway != "" {
		if o.BluetoothSide && o.TLS != nil && btPort != "" {
			add(bindSpec{Addr: net.JoinHostPort(o.Gateway, btPort), Side: SideBluetooth, TLS: true})
		}
		if o.ConnectedPort != 0 {
			add(bindSpec{Addr: net.JoinHostPort(o.Gateway, strconv.Itoa(o.ConnectedPort)), Side: SideConnected})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

type binding struct {
	spec bindSpec
	srv  *http.Server
	ln   net.Listener
}

func (b *binding) addr() string { return b.ln.Addr().String() }

// keepListeners binds what wanted() asks for and drops what it no longer
// does, now and every interval, so the LAN listener follows DHCP and the
// gateway listeners appear once the bridge has its address. A poll stands in
// for netlink notifications (D-051).
func (s *Server) keepListeners(ctx context.Context, interval time.Duration) {
	bound := map[string]*binding{}
	defer func() {
		for _, b := range bound {
			_ = b.srv.Close()
		}
	}()
	failed := map[string]string{}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		addrs, err := s.o.Addrs()
		if err != nil {
			s.log.Warn("web: list addresses", "err", err)
		} else {
			want := map[string]bindSpec{}
			for _, b := range wanted(s.o, addrs) {
				want[b.Addr] = b
			}
			for addr, b := range bound {
				if w, ok := want[addr]; !ok || w != b.spec {
					_ = b.srv.Close()
					delete(bound, addr)
					s.log.Info("web listener closed", "event", "web_unbind", "addr", addr, "side", b.spec.Side)
				}
			}
			for addr, spec := range want {
				if bound[addr] != nil {
					continue
				}
				b, err := s.bind(spec)
				if err != nil {
					if failed[addr] != err.Error() { // log each new failure once
						s.log.Warn("web listener failed", "event", "web_bind_failed", "addr", addr, "side", spec.Side, "err", err)
						failed[addr] = err.Error()
					}
					continue
				}
				delete(failed, addr)
				bound[addr] = b
				s.log.Info("web listener up", "event", "web_bind", "addr", addr, "side", spec.Side, "tls", spec.TLS)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) bind(spec bindSpec) (*binding, error) {
	ln, err := net.Listen("tcp4", spec.Addr)
	if err != nil {
		return nil, err
	}
	if spec.TLS {
		cfg := s.o.TLS.Clone()
		if spec.Side == SideBluetooth {
			cfg.MinVersion = tls.VersionTLS13 // D-039
		}
		ln = &quietTLSListener{Listener: ln, cfg: cfg}
	}
	srv := &http.Server{
		Handler:           s.Handler(spec.Side, spec.TLS),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("web listener stopped", "addr", spec.Addr, "err", err)
		}
	}()
	return &binding{spec: spec, srv: srv, ln: ln}, nil
}

// quietTLSListener wraps accepted connections in TLS so that plain HTTP on a
// TLS port gets no answer at all (docs/WEB-UI.md section 1). net/http
// otherwise answers it with "400 Client sent an HTTP request to an HTTPS
// server", written in the clear.
type quietTLSListener struct {
	net.Listener
	cfg *tls.Config
}

func (l *quietTLSListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return quietTLSConn{tls.Server(c, l.cfg)}, nil
}

type quietTLSConn struct{ *tls.Conn }

// HandshakeContext is what net/http calls. A failed handshake comes back as a
// plain error, not a tls.RecordHeaderError, which is what makes net/http
// write its 400 onto the raw socket; with a plain error it just closes.
func (c quietTLSConn) HandshakeContext(ctx context.Context) error {
	if err := c.Conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("tls handshake: %v", err) //nolint:errorlint // deliberately not wrapped
	}
	return nil
}
