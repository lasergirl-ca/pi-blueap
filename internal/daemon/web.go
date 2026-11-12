package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"path/filepath"

	"palm-gw/internal/config"
	"palm-gw/internal/setup"
	"palm-gw/internal/web"
)

// startWeb runs the web UI when [web].enabled (PLAN.md section 8.12). A
// failure here is logged and leaves the rest of the gateway running.
func (d *Daemon) startWeb(ctx context.Context) {
	cfg := d.config()
	if !cfg.Web.Enabled {
		return
	}
	tlsCfg, fp, err := d.webTLS(cfg)
	if err != nil {
		d.log.Error("web UI not started: TLS", "event", "web_failed", "err", err)
		return
	}
	s, err := web.New(web.Options{
		Config:        d.config,
		Backend:       d.ctl,
		TLS:           tlsCfg,
		Fingerprint:   fp,
		ConnectedPort: 80,
		Version:       d.opts.Version,
		Log:           d.opts.Log,
	})
	if err != nil {
		d.log.Error("web UI not started", "event", "web_failed", "err", err)
		return
	}
	go s.Run(ctx)
}

// webTLS loads the certificate for [web].tls: none for "off", the given files
// for "file", and for "self-signed" the one setup made in the state
// directory's tls/, made now if it is missing. An existing certificate is
// never replaced here, so a fingerprint the Palm app pinned stays valid.
func (d *Daemon) webTLS(cfg *config.Config) (*tls.Config, string, error) {
	var certFile, keyFile string
	switch cfg.Web.TLS {
	case "off":
		return nil, "", nil
	case "file":
		certFile, keyFile = cfg.Web.TLSCertFile, cfg.Web.TLSKeyFile
	default:
		dir := filepath.Join(filepath.Dir(d.opts.StatePath), "tls")
		certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			fp, _, err := setup.EnsureCert(dir, setup.LocalCertNames(cfg.Network.Gateway, []string{cfg.Network.Uplink}))
			if err != nil {
				return nil, "", fmt.Errorf("self-signed certificate in %s: %w", dir, err)
			}
			d.log.Info("web certificate created", "event", "web_cert_created", "dir", dir, "sha256", fp)
		}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, "", err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, "", err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, setup.Fingerprint(leaf.Raw), nil
}
