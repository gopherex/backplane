// Package config loads a service's configuration: one struct, schema from
// the Go type (schemapb), layers merged by xconf:
//
//	schema defaults < file < env <SERVICE>_* (BACKPLANE_* for the SDK block) < Consul KV
//
// Consul KV applies only to Live fields. Load reads once; Open keeps the
// configuration live.
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	jsondec "github.com/gopherex/xconf/contrib/decoders/json"
	yamldec "github.com/gopherex/xconf/contrib/decoders/yaml"
	"github.com/gopherex/xconf/contrib/sources/env"
	"github.com/gopherex/xconf/contrib/sources/file"

	"github.com/gopherex/backplane/pkg/backplane/build"
)

// EnvFile names the default configuration file.
const EnvFile = "BACKPLANE_CONFIG_FILE"

// Source names carry their kind as a prefix; provenance maps back from it.
const (
	sourceFile   = "file:"
	sourceEnv    = "env:"
	sourceConsul = "consul:"
)

// Load reads the configuration once: defaults, file and environment. For
// tools and tests; services use Open through backplane.Open.
func Load[C any](ctx context.Context, opts ...Option) (C, error) {
	var zero C

	st, err := newSettings(opts)
	if err != nil {
		return zero, err
	}

	schema, err := reflectSchema[C](st.service)
	if err != nil {
		return zero, err
	}

	sources, err := st.baseSources(reflect.TypeFor[C]())
	if err != nil {
		return zero, err
	}

	v, err := xconf.LoadAs[C](ctx, schema, sources...)
	if err != nil {
		return zero, fmt.Errorf("config: %w", err)
	}

	return v, nil
}

func newSettings(opts []Option) (*settings, error) {
	st := &settings{}
	for _, o := range opts {
		o(st)
	}

	if st.service == "" {
		st.service = build.Service
	}

	if st.service == "" {
		return nil, fmt.Errorf("config: %w (or pass config.Service())", build.ErrUnnamed)
	}

	if !st.fileSet {
		st.file = os.Getenv(EnvFile)
	}

	if !st.prefixSet {
		st.envPrefix = strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(st.service)) + "_"
	}

	return st, nil
}

// baseSources: file < BACKPLANE_* for the block (when t embeds it) <
// <SERVICE>_* < extra.
func (st *settings) baseSources(t reflect.Type) ([]xconf.Source, error) {
	var sources []xconf.Source
	if src := st.fileSource(); src != nil {
		sources = append(sources, src)
	}

	if !st.noEnv {
		if key := blockKey(t); key != "" {
			block, err := reflectSchema[Backplane]("backplane")
			if err != nil {
				return nil, err
			}

			sources = append(sources, nested{inner: st.env("BACKPLANE_"), key: key, schema: block})
		}

		sources = append(sources, st.env(st.envPrefix))
	}

	return append(sources, st.extra...), nil
}

func (st *settings) env(prefix string) xconf.Source {
	return env.New(env.Prefix(prefix), env.Name(sourceEnv+prefix))
}

func (st *settings) fileSource() xconf.Source {
	if st.noFile || st.file == "" {
		return nil
	}

	decode := jsondec.Decode
	if ext := strings.ToLower(filepath.Ext(st.file)); ext == ".yaml" || ext == ".yml" {
		decode = yamldec.Decode
	}

	return file.New(st.file, decode, file.Name(sourceFile+st.file))
}

// blockKey is the JSON name of the field of type Backplane in t; empty when
// t does not carry the block.
func blockKey(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return ""
	}

	block := reflect.TypeFor[Backplane]()

	for i := range t.NumField() {
		f := t.Field(i)
		if f.Type != block {
			continue
		}

		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			return ""
		case "":
			return f.Name
		}

		return name
	}

	return ""
}

func reflectSchema[T any](service string) (*sp.Schema, error) {
	ver, err := sp.ParseVersion(build.Version)
	if err != nil {
		ver = sp.Ver(0, 0, 0)
	}

	schema, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), "config", ver))
	if err != nil {
		return nil, fmt.Errorf("config: reflect: %w", err)
	}

	return schema, nil
}

// LivePaths lists the paths of Live fields in schema.
func LivePaths(schema *sp.Schema) []xconf.Path {
	var paths []xconf.Path

	var walk func(s *sp.Schema, prefix xconf.Path)

	walk = func(s *sp.Schema, prefix xconf.Path) {
		for _, f := range s.GetFields() {
			path := append(append(xconf.Path{}, prefix...), f.GetName())
			if f.GetAnnotations()[LiveAnnotation].GetBoolValue() {
				paths = append(paths, path)

				continue
			}

			if obj := f.GetObject(); obj != nil {
				walk(obj.GetSchema(), path)
			}
		}
	}
	walk(schema, nil)

	return paths
}
