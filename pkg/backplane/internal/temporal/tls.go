package temporal

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// TLS of the Temporal connection: PEM contents, not paths. Empty CA is the
// system pool; Cert and Key together are the client certificate (mTLS).
type TLS struct {
	Enabled            bool
	CA                 string
	Cert               string
	Key                string
	ServerName         string
	InsecureSkipVerify bool // development only
}

var (
	errCA   = errors.New("temporal: tls ca: no certificate in PEM")
	errPair = errors.New("temporal: tls: cert and key go together")
)

// config is the tls.Config of t, nil when TLS is disabled.
func (t TLS) config() (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil //nolint:nilnil // no TLS is not an error
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         t.ServerName,
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // opt-in, development only
	}

	if t.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.CA)) {
			return nil, errCA
		}

		cfg.RootCAs = pool
	}

	switch {
	case t.Cert == "" && t.Key == "":
	case t.Cert == "" || t.Key == "":
		return nil, errPair
	default:
		pair, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key))
		if err != nil {
			return nil, fmt.Errorf("temporal: tls client certificate: %w", err)
		}

		cfg.Certificates = []tls.Certificate{pair}
	}

	return cfg, nil
}
