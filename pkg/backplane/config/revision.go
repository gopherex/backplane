package config

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/hashicorp/consul/api"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
)

// revisioned wraps the Consul prefix source so the layer's revision is the
// console's config/<service>/_revision listed by the same query as the
// values: the instance state reports exactly the revision the applied
// values came from. The key itself is not decoded into the configuration.
type revisioned struct {
	inner xconf.Source
	// last is the revision seen by the latest read, whatever became of it:
	// the revision of an update the runtime rejects.
	last atomic.Uint64
}

// revisionLister is the KV list client of the prefix source: a read carrying
// a revisionHolder in its context gets the _revision value of the listing.
type revisionLister struct {
	kv  *api.KV
	key string
}

type revisionHolder struct {
	listed bool
	value  uint64
	found  bool
}

type revisionHolderKey struct{}

func (l revisionLister) List(prefix string, opts *api.QueryOptions) (api.KVPairs, *api.QueryMeta, error) {
	pairs, meta, err := l.kv.List(prefix, opts)
	if err != nil {
		return pairs, meta, err //nolint:wrapcheck // the source wraps it
	}

	if h, ok := opts.Context().Value(revisionHolderKey{}).(*revisionHolder); ok {
		h.listed = true

		for _, p := range pairs {
			if p != nil && p.Key == l.key {
				h.value, h.found = parseRevision(string(p.Value)), true
			}
		}
	}

	return pairs, meta, nil
}

func (s *revisioned) Name() string { return s.inner.Name() }

func (s *revisioned) Read(ctx context.Context, schema *sp.Schema) (xconf.Layer, error) {
	h := &revisionHolder{}

	layer, err := s.inner.Read(context.WithValue(ctx, revisionHolderKey{}, h), schema)
	if h.listed {
		s.last.Store(h.value)
	}

	switch {
	case errors.Is(err, xconf.ErrNotFound) && h.found:
		// Only the revision is there: the console cleared every override.
		return xconf.Layer{Values: map[string]any{}, Revision: formatRevision(h.value)}, nil
	case err != nil:
		return layer, err //nolint:wrapcheck // xconf wraps source errors
	}

	layer.Revision = ""
	if h.found {
		layer.Revision = formatRevision(h.value)
	}

	return layer, nil
}

func (s *revisioned) Watch(ctx context.Context, notify func()) (func(), error) {
	if w, ok := s.inner.(xconf.Watcher); ok {
		return w.Watch(ctx, notify) //nolint:wrapcheck // xconf wraps source errors
	}

	return func() {}, nil
}

func parseRevision(s string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}

	return n
}

func formatRevision(n uint64) string { return strconv.FormatUint(n, 10) }
