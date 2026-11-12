package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"

	"palm-gw/internal/setup"
)

// Role is what a login may do: the admin everything, the viewer only look
// (docs/WEB-UI.md section 3).
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

const (
	cookieName  = "palmgw_session"
	csrfField   = "csrf"
	csrfHeader  = "X-CSRF-Token"
	maxFailures = 5           // failed logins per source per window (PLAN.md section 8.12)
	failWindow  = time.Minute // the rate limit's window
)

// session is one login.
type session struct {
	token   string
	user    string
	role    Role
	csrf    string
	side    Side
	expires time.Time
	api     bool // an /api/v1 bearer token, not a browser cookie (D-058)
}

// sessions is the in-memory login table. A daemon restart logs everyone out.
type sessions struct {
	mu   sync.Mutex
	byID map[string]*session
	now  func() time.Time
}

func newSessions(now func() time.Time) *sessions {
	return &sessions{byID: map[string]*session{}, now: now}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on Linux
	}
	return hex.EncodeToString(b)
}

func (s *sessions) create(user string, role Role, side Side, ttl time.Duration) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, se := range s.byID {
		if now.After(se.expires) {
			delete(s.byID, id)
		}
	}
	se := &session{token: randomHex(32), user: user, role: role, csrf: randomHex(16), side: side, expires: now.Add(ttl)}
	s.byID[se.token] = se
	return se
}

// createAPI is create for an /api/v1 login: the token is a bearer token.
func (s *sessions) createAPI(user string, role Role, side Side, ttl time.Duration) *session {
	se := s.create(user, role, side, ttl)
	s.mu.Lock()
	se.api = true
	s.mu.Unlock()
	return se
}

// get returns the live session for token. A session is only good on the side
// it logged in on, so a cookie cannot cross from the radio to the LAN.
func (s *sessions) get(token string, side Side) *session {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.byID[token]
	if se == nil {
		return nil
	}
	if s.now().After(se.expires) {
		delete(s.byID, token)
		return nil
	}
	if se.side != side {
		return nil
	}
	return se
}

func (s *sessions) drop(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, token)
}

// limiter counts failed logins per side and source address (docs/WEB-UI.md
// section 3: per listener and per source IP).
type limiter struct {
	mu   sync.Mutex
	fail map[string][]time.Time
	now  func() time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{fail: map[string][]time.Time{}, now: now}
}

func (l *limiter) key(side Side, ip string) string { return string(side) + "|" + ip }

// recent returns the failures inside the window, pruning older ones.
func (l *limiter) recent(k string) []time.Time {
	cut := l.now().Add(-failWindow)
	ts := l.fail[k]
	i := 0
	for i < len(ts) && ts[i].Before(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fail, k)
	} else {
		l.fail[k] = ts
	}
	return ts
}

// blocked reports whether the source has used up its failures, and when the
// oldest of them leaves the window.
func (l *limiter) blocked(side Side, ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := l.recent(l.key(side, ip))
	if len(ts) < maxFailures {
		return false, 0
	}
	return true, ts[0].Add(failWindow).Sub(l.now())
}

func (l *limiter) failed(side Side, ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := l.key(side, ip)
	l.fail[k] = append(l.recent(k), l.now())
	return len(l.fail[k])
}

// dummyHash is verified against when the username is wrong, so a wrong
// username costs the same argon2id work as a wrong password (docs/WEB-UI.md
// section 3: same error, same time).
var dummyHash = sync.OnceValue(func() string {
	h, err := setup.HashPassword(randomHex(16))
	if err != nil {
		panic(err)
	}
	return h
})

// account is one login the configuration allows.
type account struct {
	user string
	hash string
	role Role
}

// check returns the role for a username and password, or "" if they do not
// match an account. Every call runs exactly one argon2id verification.
func check(accounts []account, user, password string) Role {
	hash, role := dummyHash(), Role("")
	for _, a := range accounts {
		if a.user != "" && a.hash != "" && subtle.ConstantTimeCompare([]byte(a.user), []byte(user)) == 1 {
			hash, role = a.hash, a.role
		}
	}
	ok, err := setup.VerifyPassword(password, hash)
	if err != nil || !ok {
		return ""
	}
	return role
}

// clientIP is the TCP peer's address. Headers such as X-Forwarded-For are
// never consulted (docs/WEB-UI.md section 1).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
