package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	consulsrc "github.com/gopherex/xconf/contrib/sources/consul"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

// Runtime is a loaded, live configuration. Value is stable for the life of
// the runtime: static fields never change, Live fields change in place.
type Runtime[C any] struct {
	value  *C
	schema *sp.Schema
	live   []xconf.Path
	rt     *xconf.TypedRuntime[C]
	stop   context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	mu       sync.Mutex
	rejected error
	changes  []func()
}

// Effective is what the instance runs with: masked values, the layer each
// path came from, and the last rejected update.
type Effective struct {
	Values  []byte
	Sources map[string]backplanev1.ConfigSource
	Err     error
}

// Open loads the configuration and keeps it live: Consul KV (named by the
// embedded Backplane block) overrides Live fields only. The Consul layer is
// resilient: unreachable at start it is empty, failing later it keeps its
// last values; either way Open succeeds, Degraded reports it and the layer
// recovers on its own. Close it when done.
func Open[C Backplaner](ctx context.Context, opts ...Option) (*Runtime[C], error) {
	st, err := newSettings(opts)
	if err != nil {
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

	first, err := xconf.LoadAs[C](ctx, schema, sources...)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	live := LivePaths(schema)

	if consul := first.BackplaneConfig().Consul; consul.Enabled() && !st.noConsul {
		src, err := st.consulSource(consul, live)
		if err != nil {
			return nil, err
		}

		sources = append(sources, src)
	}

	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))

	rt, err := xconf.OpenAs[C](runCtx, schema, sources...)
	if err != nil {
		stop()

		return nil, fmt.Errorf("config: %w", err)
	}

	value, err := rt.Current()
	if err != nil {
		stop()

		return nil, errors.Join(fmt.Errorf("config: decode: %w", err), rt.Close())
	}

	r := &Runtime[C]{value: &value, schema: schema, live: live, rt: rt, stop: stop}
	r.wg.Add(1)

	go r.forward(runCtx)

	return r, nil
}

func (st *settings) consulSource(c Consul, live []xconf.Path) (xconf.Source, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}

	prefix := "config/" + st.service + "/"
	opts := append([]consulsrc.Option{consulsrc.Name(sourceConsul + prefix)}, st.consulOpts...)

	var retry []xconf.ResilientOption
	if st.retryMin > 0 {
		retry = append(retry, xconf.Backoff(st.retryMin, st.retryMax))
	}

	src := xconf.Optional(xconf.AllowPaths(consulsrc.NewPrefix(client.KV(), prefix, opts...), live...))

	return xconf.Resilient(src, retry...), nil
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
	if replace, ok := liveReplace(dst); ok {
		replace.Call([]reflect.Value{next.Addr()})

		return
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

// liveReplace returns dst's Replace method when dst is a Live field: a type
// of this package whose pointer has Replace(*Self).
func liveReplace(dst reflect.Value) (reflect.Value, bool) {
	if !dst.CanAddr() || dst.Type().PkgPath() != reflect.TypeFor[Backplane]().PkgPath() {
		return reflect.Value{}, false
	}

	ptr := dst.Addr()

	method := ptr.MethodByName("Replace")
	if !method.IsValid() || method.Type().NumIn() != 1 || method.Type().In(0) != ptr.Type() {
		return reflect.Value{}, false
	}

	return method, true
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

// Schema is the configuration schema; Live fields carry LiveAnnotation.
func (r *Runtime[C]) Schema() *sp.Schema { return r.schema }

// Live lists the paths of Live fields.
func (r *Runtime[C]) Live() []xconf.Path { return append([]xconf.Path(nil), r.live...) }

// OnChange calls fn whenever Effective may have changed.
func (r *Runtime[C]) OnChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.changes = append(r.changes, fn)
}

// Degraded reports why the Consul layer is stale or empty; nil when healthy
// or not configured.
func (r *Runtime[C]) Degraded() error {
	for name, err := range r.rt.Snapshot().Degraded() {
		if strings.HasPrefix(name, sourceConsul) {
			return err
		}
	}

	return nil
}

// Effective reports masked values, provenance and the last rejected update.
func (r *Runtime[C]) Effective() Effective {
	r.mu.Lock()
	rejected := r.rejected
	r.mu.Unlock()

	snap := r.rt.Snapshot()
	eff := Effective{Sources: map[string]backplanev1.ConfigSource{}, Err: rejected}

	values, err := json.Marshal(snap.Baked().Masked().ToGo())
	eff.Values, eff.Err = values, errors.Join(rejected, err)

	for ptr, origins := range snap.Origins() {
		src := backplanev1.ConfigSource_CONFIG_SOURCE_DEFAULT
		for _, o := range origins {
			src = max(src, sourceOf(o.Source))
		}

		eff.Sources[strings.ReplaceAll(strings.TrimPrefix(ptr, "/"), "/", ".")] = src
	}

	return eff
}

func sourceOf(name string) backplanev1.ConfigSource {
	switch {
	case strings.HasPrefix(name, sourceConsul):
		return backplanev1.ConfigSource_CONFIG_SOURCE_KV
	case strings.HasPrefix(name, sourceEnv):
		return backplanev1.ConfigSource_CONFIG_SOURCE_ENV
	case strings.HasPrefix(name, sourceFile):
		return backplanev1.ConfigSource_CONFIG_SOURCE_FILE
	}

	return backplanev1.ConfigSource_CONFIG_SOURCE_DEFAULT
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
