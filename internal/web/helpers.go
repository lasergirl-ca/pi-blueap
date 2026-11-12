package web

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"net"
	"time"
)

func subtleEq(a, b string) int { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) }

func splitPort(hostport string) (string, string, error) { return net.SplitHostPort(hostport) }

var funcs = template.FuncMap{
	"since": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return duration(time.Since(t))
	},
	"secs": func(n int64) string { return duration(time.Duration(n) * time.Second) },
	"yesno": func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	},
}

// duration is 3d4h, 2h05m, 4m10s or 9s.
func duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
