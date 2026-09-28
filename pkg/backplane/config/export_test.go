package config

import (
	"crypto/tls"
	"net/http"

	"github.com/hashicorp/consul/api"
)

// ConsulAPIConfig is the client configuration the block maps to.
func ConsulAPIConfig(c Consul) (*api.Config, error) { return c.apiConfig() }

// ConsulClientTLS builds the client's HTTP client and returns the TLS
// settings of its transport.
func ConsulClientTLS(c Consul) (*tls.Config, error) {
	cfg, err := c.apiConfig()
	if err != nil {
		return nil, err
	}

	hc, err := api.NewHttpClient(cfg.Transport, cfg.TLSConfig)
	if err != nil {
		return nil, err
	}

	return hc.Transport.(*http.Transport).TLSClientConfig, nil
}
