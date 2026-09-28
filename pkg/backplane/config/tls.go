package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// Errors of TLS.ClientConfig.
var (
	// ErrTLSCA: the CA holds no PEM certificate.
	ErrTLSCA = errors.New("config: tls: ca holds no PEM certificate")
	// ErrTLSPair: only one of cert and key is set.
	ErrTLSPair = errors.New("config: tls: cert and key go together")
)

// ClientConfig is the client TLS configuration of t — what the SDK's
// Consul, NATS and Temporal connections use; nil when TLS is disabled. An
// empty CA is the system pool, an empty ServerName the host dialed; Cert
// and Key together are the client certificate.
func (t TLS) ClientConfig() (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil //nolint:nilnil // no TLS is not an error
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         t.ServerName,
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // explicit development switch
	}

	if t.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.CA)) {
			return nil, ErrTLSCA
		}

		cfg.RootCAs = pool
	}

	if (t.Cert == "") != (t.Key == "") {
		return nil, ErrTLSPair
	}

	if t.Cert != "" {
		pair, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key.Reveal()))
		if err != nil {
			return nil, fmt.Errorf("config: tls: client certificate: %w", err)
		}

		cfg.Certificates = []tls.Certificate{pair}
	}

	return cfg, nil
}
