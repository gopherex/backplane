package temporal

import (
	"crypto/tls"
	"fmt"

	"github.com/gopherex/backplane/pkg/backplane/config"
)

// TLS of the Temporal connection: PEM contents, not paths. Empty CA is the
// system pool; Cert and Key together are the client certificate (mTLS).
// The API key is separate (Params.APIKey): it rides on the connection as
// credentials and makes the Temporal SDK turn TLS on by itself.
type TLS struct {
	Enabled            bool
	CA                 string
	Cert               string
	Key                string
	ServerName         string
	InsecureSkipVerify bool // development only
}

// config is the tls.Config of t, nil when TLS is disabled: the same
// client configuration the SDK builds for Consul and NATS.
func (t TLS) config() (*tls.Config, error) {
	cfg, err := config.TLS{
		Enabled: t.Enabled, CA: t.CA, Cert: t.Cert, Key: config.Secret(t.Key),
		ServerName: t.ServerName, InsecureSkipVerify: t.InsecureSkipVerify,
	}.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("temporal: %w", err)
	}

	return cfg, nil
}
