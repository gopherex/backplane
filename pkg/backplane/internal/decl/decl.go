// Package decl is shared by the hook, activity and event packages: it
// resolves the service a Scope belongs to, derives payload schemas and
// encodes payloads.
package decl

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

// Env returns the service env and node of scope; a zero scope panics.
func Env(scope any, what string) (*env.Env, *node.Node) {
	n := link.NodeOf(scope)
	if n == nil {
		panic(fmt.Sprintf("backplane: %s declared on a zero scope: use the Root or a component", what))
	}

	e := env.Of(n)
	if e == nil {
		panic(fmt.Sprintf("backplane: %s declared on a scope without a service", what))
	}

	return e, n
}

// Schema reflects T under <service>/<name>@1.0.0, or nil when T cannot be
// described: the declaration stays, the payload is plain JSON.
func Schema[T any](service, name string) *sp.Schema {
	s, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0)))
	if err != nil {
		return nil
	}

	return s
}

// Encode is protojson for proto messages, encoding/json otherwise.
func Encode(v any) ([]byte, error) {
	if m, ok := v.(proto.Message); ok {
		return protojson.Marshal(m) //nolint:wrapcheck // one encoder per path
	}

	return json.Marshal(v) //nolint:wrapcheck // one encoder per path
}

// Decode is the inverse of Encode.
func Decode(data []byte, v any) error {
	if m, ok := v.(proto.Message); ok {
		return protojson.Unmarshal(data, m) //nolint:wrapcheck // one decoder per path
	}

	return json.Unmarshal(data, v) //nolint:wrapcheck // one decoder per path
}
