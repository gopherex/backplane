// Package manifest collects a service's declarations into a
// backplanev1.Manifest. Declarations are recorded as they happen; the first
// error is kept and returned by Build.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"runtime/debug"
	"sort"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

const sdkModule = "github.com/gopherex/backplane"

// Builder accumulates declarations.
type Builder struct {
	mu  sync.Mutex
	m   *backplanev1.Manifest
	err error
}

// New starts a manifest for service@version.
func New(service, version string) *Builder {
	return &Builder{m: &backplanev1.Manifest{Service: service, Version: version, SdkVersion: sdkVersion()}}
}

func (b *Builder) with(fn func(m *backplanev1.Manifest) error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.err != nil {
		return
	}

	b.err = fn(b.m)
}

// Config records the configuration schema and its live paths.
func (b *Builder) Config(schema *sp.Schema, live []string) {
	b.with(func(m *backplanev1.Manifest) error {
		m.Config = section(schema)
		m.Config.Live = live

		return nil
	})
}

// Nodes records the node tree.
func (b *Builder) Nodes(nodes []*backplanev1.Node) {
	b.with(func(m *backplanev1.Manifest) error {
		m.Nodes = nodes

		return nil
	})
}

// Route records a route.
func (b *Builder) Route(r *backplanev1.Route) {
	b.with(func(m *backplanev1.Manifest) error { m.Routes = append(m.Routes, r); return nil })
}

// GRPC records one route per gRPC service with derived descriptors.
func (b *Builder) GRPC(services []string, kind backplanev1.RouteKind, port uint32, host string) {
	b.with(func(m *backplanev1.Manifest) error {
		fds, err := Descriptors(services)
		if err != nil {
			return err
		}

		for _, name := range services {
			r := &backplanev1.Route{Kind: kind, Port: port, Schema: &backplanev1.Route_Descriptors{Descriptors: fds}}
			if host != "" {
				r.Match = &backplanev1.Route_Host{Host: host}
			} else {
				r.Match = &backplanev1.Route_Prefix{Prefix: "/" + name + "/"}
			}

			m.Routes = append(m.Routes, r)
		}

		return nil
	})
}

// Internal records internal API service names.
func (b *Builder) Internal(services []string) {
	b.with(func(m *backplanev1.Manifest) error {
		m.InternalServices = append(m.InternalServices, services...)
		return nil
	})
}

// Hook, Activity and Event record the corresponding declarations.
func (b *Builder) Hook(h *backplanev1.Hook) {
	b.with(func(m *backplanev1.Manifest) error { m.Hooks = append(m.Hooks, h); return nil })
}

func (b *Builder) Activity(a *backplanev1.Activity) {
	b.with(func(m *backplanev1.Manifest) error { m.Activities = append(m.Activities, a); return nil })
}

func (b *Builder) Event(e *backplanev1.Event) {
	b.with(func(m *backplanev1.Manifest) error { m.Events = append(m.Events, e); return nil })
}

// UI records the bundle hash and sdk_major from plugin.json.
func (b *Builder) UI(bundle fs.FS) {
	b.with(func(m *backplanev1.Manifest) error {
		ui, err := uiOf(bundle)
		m.Ui = ui

		return err
	})
}

// Fail records an error found outside the builder.
func (b *Builder) Fail(err error) { b.with(func(*backplanev1.Manifest) error { return err }) }

// Build returns a copy of the manifest or the first declaration error.
func (b *Builder) Build() (*backplanev1.Manifest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.err != nil {
		return nil, b.err
	}

	sort.Strings(b.m.GetInternalServices())

	return proto.CloneOf(b.m), nil
}

// Services returns the names srv registered as a side effect of register:
// what register added, not what was there before.
func Services(srv *grpc.Server, register func(grpc.ServiceRegistrar)) []string {
	before := srv.GetServiceInfo()
	register(srv)

	var added []string

	for name := range srv.GetServiceInfo() {
		if _, had := before[name]; !had {
			added = append(added, name)
		}
	}

	sort.Strings(added)

	return added
}

func section(schema *sp.Schema) *backplanev1.ConfigSection {
	sec := &backplanev1.ConfigSection{Schema: schema}
	for _, f := range schema.GetFields() {
		sec.Keys = append(sec.Keys, f.GetName())
	}

	return sec
}

// Descriptors serializes the files of the named services with their
// transitive imports from the global registry.
func Descriptors(services []string) ([]byte, error) {
	seen := map[string]bool{}

	var (
		files []*descriptorpb.FileDescriptorProto
		add   func(fd protoreflect.FileDescriptor)
	)

	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}

		seen[fd.Path()] = true
		for i := range fd.Imports().Len() {
			add(fd.Imports().Get(i).FileDescriptor)
		}

		files = append(files, protodesc.ToFileDescriptorProto(fd))
	}

	for _, name := range services {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			return nil, fmt.Errorf("manifest: descriptors of %s: %w", name, err)
		}

		add(d.ParentFile())
	}

	fds, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: files})
	if err != nil {
		return nil, fmt.Errorf("manifest: marshal descriptors: %w", err)
	}

	return fds, nil
}

func uiOf(bundle fs.FS) (*backplanev1.UI, error) {
	h := sha256.New()

	err := fs.WalkDir(bundle, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := fs.ReadFile(bundle, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		_, _ = io.WriteString(h, path+"\x00")
		_, _ = h.Write(data)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("manifest: ui bundle: %w", err)
	}

	ui := &backplanev1.UI{Hash: hex.EncodeToString(h.Sum(nil))}

	raw, err := fs.ReadFile(bundle, "plugin.json")
	if err != nil {
		return nil, fmt.Errorf("manifest: ui bundle: %w", err)
	}

	var plugin struct {
		SDKMajor uint32 `json:"sdk_major"`
	}
	if err := json.Unmarshal(raw, &plugin); err != nil {
		return nil, fmt.Errorf("manifest: plugin.json: %w", err)
	}

	ui.SdkMajor = plugin.SDKMajor

	return ui, nil
}

func sdkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}

	if bi.Main.Path == sdkModule {
		return bi.Main.Version
	}

	for _, d := range bi.Deps {
		if d.Path == sdkModule {
			return d.Version
		}
	}

	return "(devel)"
}
