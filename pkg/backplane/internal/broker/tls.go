package broker

import (
	"crypto/tls"
	"fmt"

	"github.com/gopherex/backplane/pkg/backplane/config"
)

// TLS of the NATS connection. PEM contents, not paths; an empty CA means
// the system pool.
type TLS struct {
	Enabled            bool
	CA                 string
	Cert               string
	Key                string
	ServerName         string
	InsecureSkipVerify bool // development only
}

// Errors of TLS.
var (
	// ErrTLSCA: the CA holds no PEM certificate.
	ErrTLSCA = config.ErrTLSCA
	// ErrTLSPair: only one of cert and key is set.
	ErrTLSPair = config.ErrTLSPair
)

// tlsConfig maps t to the client configuration; nil when TLS is off.
func tlsConfig(t TLS) (*tls.Config, error) {
	cfg, err := config.TLS{
		Enabled: t.Enabled, CA: t.CA, Cert: t.Cert, Key: config.Secret(t.Key),
		ServerName: t.ServerName, InsecureSkipVerify: t.InsecureSkipVerify,
	}.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("broker: %w", err)
	}

	return cfg, nil
}
