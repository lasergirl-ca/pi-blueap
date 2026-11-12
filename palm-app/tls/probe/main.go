// Command probe checks the Palm app's TLS client (palm-app/tls) on the
// build machine against a server set up as palm-gw's: TLS 1.3, ALPN
// http/1.1, the self-signed ECDSA P-256 certificate palm-gw setup makes,
// and a /api/v1/status?format=text reply in Windows-1252. It runs the
// client three times: first use (no pins), the right pin among others,
// and a wrong pin, which must be refused (docs/PALM-APP.md, D-062).
//
//	go run ./palm-app/tls/probe palm-app/tls/build/host_test
package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"palm-gw/internal/setup"
	"palm-gw/internal/web"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: probe HOST_TEST")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func run(client string) error {
	dir, err := os.MkdirTemp("", "palmgw-probe")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fp, _, err := setup.EnsureCert(dir, setup.CertNames{IPs: []net.IP{net.IPv4(127, 0, 0, 1)}, Gateway: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(spki[:])
	wantCert := strings.ToLower(strings.ReplaceAll(fp, ":", ""))

	ln, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	})
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=windows-1252")
		_, _ = w.Write(web.Windows1252("status=ok\r\ngateway=Zoë's Probe\r\n\r\n"))
	})
	go func() { _ = http.Serve(ln, mux) }()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)

	call := func(pins ...string) (string, int, error) {
		args := append([]string{"127.0.0.1", port, "/api/v1/status?format=text"}, pins...)
		out, err := exec.Command(client, args...).CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code, err = ee.ExitCode(), nil
		}
		return string(out), code, err
	}

	// First use: any key, and the client reports what to show and pin.
	out, code, err := call()
	if err != nil || code != 0 {
		return fmt.Errorf("first use: exit %d %w\n%s", code, err, out)
	}
	for _, want := range []string{"spki " + pin, "cert " + wantCert, "match -1", "HTTP/1.1 200 OK", "gateway=Zo\xebs Probe"} {
		want = strings.Replace(want, "Zo\xebs", "Zo\xeb's", 1)
		if !strings.Contains(out, want) {
			return fmt.Errorf("first use: no %q in\n%s", want, out)
		}
	}
	fmt.Printf("first use: spki and certificate fingerprint reported, reply read (%s)\n", line(out, "heap"))

	// Pinned: the right pin second of three, so match says 1.
	other := strings.Repeat("ab", 32)
	out, code, err = call(other, pin, strings.Repeat("cd", 32))
	if err != nil || code != 0 || !strings.Contains(out, "match 1") {
		return fmt.Errorf("pinned: exit %d %w\n%s", code, err, out)
	}
	fmt.Println("pinned: matched the second of three pins")

	// A key that matches no pin is refused before any request.
	out, code, err = call(other)
	if err != nil || code != 2 || strings.Contains(out, "HTTP/1.1") {
		return fmt.Errorf("wrong pin: exit %d %w\n%s", code, err, out)
	}
	fmt.Println("wrong pin: refused")
	return nil
}

func line(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix+" ") {
			return l
		}
	}
	return ""
}
