package registry

import (
	"context"
	"fmt"

	"github.com/hashicorp/consul/api"
)

// Absent confirms that no instance is registered (healthy or otherwise) and
// no instance state remains in Consul. It uses consistent reads, not a cached
// or stale snapshot. An outage prevents cleanup. Manifests do not imply life.
func (r *Registry) Absent(ctx context.Context, service string) (bool, error) {
	opts := (&api.QueryOptions{RequireConsistent: true}).WithContext(ctx)

	entries, _, err := r.client.Health().Service(service, "", false, opts)
	if err != nil {
		return false, fmt.Errorf("service health: %w", err)
	}

	if len(entries) > 0 {
		return false, nil
	}

	keys, _, err := r.client.KV().Keys(Prefix+service+"/instances/", "", opts)
	if err != nil {
		return false, fmt.Errorf("instance states: %w", err)
	}

	return len(keys) == 0, nil
}
