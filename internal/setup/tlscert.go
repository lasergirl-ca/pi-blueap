package setup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TLSDir holds the self-signed web certificate (PLAN.md section 8.12).
const TLSDir = "/var/lib/palm-gw/tls"

// CertNames are the names a self-signed certificate covers: the host name
// (and its .local form), the Pi's current addresses, and the gateway address,
// so the same certificate serves the Bluetooth-side listener (D-039).
//
// Gateway is the address the Palm app and the Bluetooth side use. An
// existing certificate is kept as long as it covers the host names and
// Gateway, even when the LAN addresses differ: a new uplink or DHCP address
// must not change the fingerprint the Palm app pinned (D-059). With no
// Gateway, every address in IPs must be covered.
type CertNames struct {
	DNS     []string
	IPs     []net.IP
	Gateway net.IP
}

// coveredBy reports whether certificate c serves every name n requires.
func (n CertNames) coveredBy(c *x509.Certificate) bool {
	has := map[string]bool{}
	for _, d := range c.DNSNames {
		has["dns:"+strings.ToLower(d)] = true
	}
	for _, ip := range c.IPAddresses {
		has["ip:"+ip.String()] = true
	}
	need := []string{}
	for _, d := range n.DNS {
		need = append(need, "dns:"+strings.ToLower(d))
	}
	ips := n.IPs
	if n.Gateway != nil {
		ips = []net.IP{n.Gateway}
	}
	for _, ip := range ips {
		need = append(need, "ip:"+ip.String())
	}
	for _, k := range need {
		if !has[k] {
			return false
		}
	}
	return true
}

// EnsureCert writes cert.pem and key.pem under dir unless a certificate that
// covers the names n requires (see CertNames) and is valid for at least 30
// more days is already there. It returns the certificate's SHA-256 fingerprint and whether it made
// a new one.
func EnsureCert(dir string, names CertNames) (fingerprint string, created bool, err error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if data, err := os.ReadFile(certPath); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if c, err := x509.ParseCertificate(block.Bytes); err == nil &&
				time.Until(c.NotAfter) > 30*24*time.Hour && names.coveredBy(c) {
				if _, err := os.Stat(keyPath); err == nil {
					return Fingerprint(c.Raw), false, nil
				}
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", false, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", false, err
	}
	cn := "palm-gw"
	if len(names.DNS) > 0 {
		cn = names.DNS[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names.DNS,
		IPAddresses:           names.IPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", false, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", false, err
	}
	if _, err := writeIfChanged(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", false, err
	}
	if _, err := writeIfChanged(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", false, err
	}
	return Fingerprint(der), true, nil
}

// Fingerprint is the colon-separated SHA-256 of a DER certificate, the form
// browsers show and the Palm app pins.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// LocalCertNames gathers the host name and the IPv4 addresses of the given
// interfaces, plus the gateway address.
func LocalCertNames(gateway string, ifaces []string) CertNames {
	var n CertNames
	if h, err := os.Hostname(); err == nil && h != "" {
		n.DNS = append(n.DNS, h)
		if !strings.Contains(h, ".") {
			n.DNS = append(n.DNS, h+".local")
		}
	}
	seen := map[string]bool{}
	add := func(ip net.IP) {
		if ip == nil || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		n.IPs = append(n.IPs, ip)
	}
	for _, name := range ifaces {
		ifc, err := net.InterfaceByName(name)
		if err != nil {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				add(ipn.IP.To4())
			}
		}
	}
	n.Gateway = net.ParseIP(gateway).To4()
	add(n.Gateway)
	return n
}
