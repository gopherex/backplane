package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/hashicorp/consul/api"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	consulsrc "github.com/gopherex/xconf/contrib/sources/consul"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/configrt"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
)

//nolint:gochecknoinits // installs the private accessors for the SDK core and backplanetest
func init() {
	link.ConfigState = func(rt any) configrt.State {
		if s, ok := rt.(interface{ state() configrt.State }); ok {
			return s.state()
		}

		return nil
	}
	link.SetLive = func(live, v any) {
		if l, ok := live.(interface{ setAny(v any) }); ok {
			l.setAny(v)
		}
	}
}

// Runtime is a loaded, live configuration. Value is stable for the life of
// the runtime: static fields never change, Live fields change in place.
type Runtime[C any] struct {
	value  *C
	schema *sp.Schema
	live   [][]string
	rt     *xconf.TypedRuntime[C]
	consul *api.Client
	stop   context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	mu       sync.Mutex
	rejected error
	changes  []func()
}

// Open loads the configuration and keeps it live: Consul KV (named by the
// embedded Backplane block) overrides Live fields only. The Consul layer is
// resilient: unreachable at start it is empty, failing later it keeps its
// last values; Degraded reports it and the layer recovers on its own. A
// required Live value that only Consul holds keeps Open waiting until
// Consul answers or ctx ends. The runtime outlives ctx; Close it when done.
func Open[C Backplaner](ctx context.Context, opts ...Option) (*Runtime[C], error) {
	st, err := newSettings(opts)
	if err != nil {
		return nil, err
	}

	if err := checkLive(reflect.TypeFor[C](), reflect.TypeFor[C]().Name(), map[reflect.Type]bool{}); err != nil {
		return nil, err
	}

	schema, err := reflectSchema[C](st.service)
	if err != nil {
		return nil, err
	}

	sources, err := st.baseSources(reflect.TypeFor[C]())
	if err != nil {
		return nil, err
	}

	// The first pass only finds Consul; Live fields may still be waiting in KV.
	first, err := xconf.LoadAs[C](ctx, configrt.WithoutLiveRequired(schema), sources...)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	live := configrt.LivePaths(schema)
	r := &Runtime[C]{schema: schema, live: live}

	if consul := first.BackplaneConfig().Consul; consul.Enabled() && !st.noConsul {
		r.consul, err = configrt.ConsulClient(consul.Addr, consul.Token.Reveal())
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}

		sources = append(sources, st.consulSource(r.consul, live))
	}

	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	unbind := context.AfterFunc(ctx, stop)

	rt, err := xconf.OpenAs[C](runCtx, schema, sources...)

	unbind()

	if err != nil {
		stop()

		return nil, fmt.Errorf("config: %w", errors.Join(err, ctx.Err()))
	}

	value, err := rt.Current()
	if err != nil {
		stop()

		return nil, errors.Join(fmt.Errorf("config: decode: %w", err), rt.Close())
	}

	r.value, r.rt, r.stop = &value, rt, stop
	r.wg.Add(1)

	go r.forward(runCtx)

	return r, nil
}

func (st *settings) consulSource(client *api.Client, live [][]string) xconf.Source {
	prefix := "config/" + st.service + "/"
	opts := append([]consulsrc.Option{consulsrc.Name(configrt.SourceConsul + prefix)}, st.consulOpts...)

	paths := make([]xconf.Path, len(live))
	for i, p := range live {
		paths[i] = p
	}

	var backoff []xconf.ResilientOption
	if st.backoffMin > 0 {
		backoff = append(backoff, xconf.Backoff(st.backoffMin, st.backoffMax))
	}

	src := xconf.Optional(xconf.AllowPaths(consulsrc.NewPrefix(client.KV(), prefix, opts...), paths...))

	return xconf.Resilient(src, backoff...)
}

// forward applies the runtime's snapshots to Value.
func (r *Runtime[C]) forward(ctx context.Context) {
	defer r.wg.Done()

	for ev := range r.rt.Subscribe(ctx) {
		next, err := xconf.Decode[C](ev.Snapshot)
		if ev.Err != nil {
			err = ev.Err
		}

		r.mu.Lock()
		r.rejected = err
		r.mu.Unlock()

		if err == nil {
			transfer(reflect.ValueOf(r.value).Elem(), reflect.ValueOf(&next).Elem())
		}

		r.notify()
	}
}

// transfer publishes every Live field of next into dst, in place.
func transfer(dst, next reflect.Value) {
	if dst.CanAddr() {
		if l, ok := dst.Addr().Interface().(liveField); ok {
			l.replaceFrom(next.Addr().Interface())

			return
		}
	}

	switch dst.Kind() {
	case reflect.Struct:
		for i := range dst.NumField() {
			if dst.Type().Field(i).IsExported() {
				transfer(dst.Field(i), next.Field(i))
			}
		}
	case reflect.Pointer:
		if !dst.IsNil() && !next.IsNil() {
			transfer(dst.Elem(), next.Elem())
		}
	default:
	}
}

func (r *Runtime[C]) notify() {
	r.mu.Lock()
	changes := append([]func(){}, r.changes...)
	r.mu.Unlock()

	for _, fn := range changes {
		fn()
	}
}

// Value is the configuration. The pointer is stable; Live fields update in
// place.
func (r *Runtime[C]) Value() *C { return r.value }

// Degraded reports why the Consul layer is stale or empty; nil when healthy
// or not configured.
func (r *Runtime[C]) Degraded() error {
	for name, err := range r.rt.Snapshot().Degraded() {
		if strings.HasPrefix(name, configrt.SourceConsul) {
			return err
		}
	}

	return nil
}

// Close stops watching; safe to call more than once.
func (r *Runtime[C]) Close() error {
	var err error

	r.once.Do(func() {
		r.stop()

		if cerr := r.rt.Close(); cerr != nil {
			err = fmt.Errorf("config: close: %w", cerr)
		}

		r.wg.Wait()
	})

	return err
}

//nolint:ireturn // the SDK's view is an interface by design
func (r *Runtime[C]) state() configrt.State { return state[C]{r} }

// state is the SDK's view, unreachable for authors.
type state[C any] struct{ r *Runtime[C] }

func (s state[C]) Schema() *sp.Schema { return s.r.schema }

func (s state[C]) LivePaths() []string {
	out := make([]string, len(s.r.live))
	for i, p := range s.r.live {
		out[i] = strings.Join(p, ".")
	}

	return out
}

func (s state[C]) OnChange(fn func()) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()

	s.r.changes = append(s.r.changes, fn)
}

func (s state[C]) Degraded() error { return s.r.Degraded() }

func (s state[C]) Consul() *api.Client { return s.r.consul }

func (s state[C]) Close() error { return s.r.Close() }

func (s state[C]) Effective() configrt.Effective {
	s.r.mu.Lock()
	rejected := s.r.rejected
	s.r.mu.Unlock()

	snap := s.r.rt.Snapshot()
	eff := configrt.Effective{Sources: map[string]backplanev1.ConfigSource{}}

	values, err := json.Marshal(snap.Baked().Masked().ToGo())
	eff.Values, eff.Err = values, errors.Join(rejected, err)

	for ptr, origins := range snap.Origins() {
		src := backplanev1.ConfigSource_CONFIG_SOURCE_DEFAULT
		for _, o := range origins {
			src = max(src, configrt.SourceOf(o.Source))
		}

		eff.Sources[strings.ReplaceAll(strings.TrimPrefix(ptr, "/"), "/", ".")] = src
	}

	return eff
}
