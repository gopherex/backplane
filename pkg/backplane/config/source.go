package config

import (
	"context"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
)

// nested reads an inner source against its own schema and places the layer
// under key: the fixed BACKPLANE_* environment for the embedded block.
type nested struct {
	inner  xconf.Source
	key    string
	schema *sp.Schema
}

func (n nested) Name() string { return n.inner.Name() }

func (n nested) Read(ctx context.Context, _ *sp.Schema) (xconf.Layer, error) {
	layer, err := n.inner.Read(ctx, n.schema)
	if err != nil {
		return xconf.Layer{}, err
	}

	locations := make(map[string]xconf.Location, len(layer.Locations))
	for ptr, loc := range layer.Locations {
		locations["/"+n.key+ptr] = loc
	}

	return xconf.Layer{Values: map[string]any{n.key: layer.Values}, Revision: layer.Revision, Locations: locations}, nil
}
