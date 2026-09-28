package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	consulsrc "github.com/gopherex/xconf/contrib/sources/consul"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

const defaultRetry = 30 * time.Second

// Runtime is a loaded, live configuration. Value is stable for the life of
// the runtime: static fields never change, Live fields change in place.
type Runtime[C any] struct {
	value  *C
	schema *sp.Schema
	live   []xconf.Path
	open   func(withConsul bool) (*xconf.TypedRuntime[C], error)
	retry  time.Duration
	stop   context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	mu       sync.Mutex
	rt       *xconf.TypedRuntime[C]
	degraded error
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
// embedded Backplane block) overrides Live fields only. When Consul is
// configured but unreachable, Open succeeds without that layer, reports it
// through Degraded and keeps retrying. Close it when done.
func Open[C Backplaner](ctx context.Context, opts ...Option) (*Runtime[C], error) {
	st, err := newSettings(opts)
	if err != nil {
		return nil, err
	}

	schema, err := reflectSchema[C](st.service)
	if err != nil {
		return nil, err
	}

	base, err := st.baseSources(reflect.TypeFor[C]())
	if err != nil {
		return nil, err
	}

	first, err := xconf.LoadAs[C](ctx, schema, base...)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	consul := first.BackplaneConfig().Consul
	withConsul := consul.Enabled() && !st.noConsul
	live := LivePaths(schema)

	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	r := &Runtime[C]{schema: schema, live: live, retry: st.retryEvery, stop: stop}

	if r.retry <= 0 {
		r.retry = defaultRetry
	}

	r.open = func(withConsul bool) (*xconf.TypedRuntime[C], error) {
		sources := base

		if withConsul {
			src, err := st.consulSource(consul, live)
			if err != nil {
				return nil, err
			}

			sources = append(append([]xconf.Source{}, base...), src)
		}

		return xconf.OpenAs[C](runCtx, schema, sources...)
	}

	if err := r.load(runCtx, withConsul); err != nil {
		stop()

		return nil, err
	}

	if r.degraded != nil {
		r.wg.Add(1)

		go r.repair(runCtx)
	}

	return r, nil
}

// load opens the first runtime, without Consul when it is unreachable.
func (r *Runtime[C]) load(ctx context.Context, withConsul bool) error {
	rt, err := r.open(withConsul)
	if err != nil && withConsul && fromConsul(err) {
		r.degraded = err
		rt, err = r.open(false)
	}

	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	value, err := rt.Current()
	if err != nil {
		return errors.Join(fmt.Errorf("config: decode: %w", err), rt.Close())
	}

	r.value = &value
	r.adopt(ctx, rt)

	return nil
}

func (st *settings) consulSource(c Consul, live []xconf.Path) (xconf.Source, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}

	prefix := "config/" + st.service + "/"
	opts := append([]consulsrc.Option{consulsrc.Name(sourceConsul + prefix)}, st.consulOpts...)

	return xconf.Optional(xconf.AllowPaths(consulsrc.NewPrefix(client.KV(), prefix, opts...), live...)), nil
}

func fromConsul(err error) bool {
	var se *xconf.SourceError

	return errors.As(err, &se) && strings.HasPrefix(se.Source, sourceConsul)
}

// adopt makes rt current and applies its updates.
func (r *Runtime[C]) adopt(ctx context.Context, rt *xconf.TypedRuntime[C]) {
	r.mu.Lock()
	old := r.rt
	r.rt = rt
	r.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}

	r.wg.Add(1)

	go r.forward(ctx, rt)
}

// forward applies rt's snapshots while rt is current.
func (r *Runtime[C]) forward(ctx context.Context, rt *xconf.TypedRuntime[C]) {
	defer r.wg.Done()

	for ev := range rt.Subscribe(ctx) {
		next, err := xconf.Decode[C](ev.Snapshot)
		if ev.Err != nil {
			err = ev.Err
		}

		r.mu.Lock()
		if r.rt != rt {
			r.mu.Unlock()

			return
		}

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

// repair retries the Consul layer until it opens, then swaps runtimes.
func (r *Runtime[C]) repair(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.retry)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		rt, err := r.open(true)
		if err == nil {
			var next C

			if next, err = rt.Current(); err == nil {
				transfer(reflect.ValueOf(r.value).Elem(), reflect.ValueOf(&next).Elem())
				r.adopt(ctx, rt)
			} else {
				_ = rt.Close()
			}
		}

		r.mu.Lock()
		r.degraded = err
		r.mu.Unlock()

		if err == nil {
			r.notify()

			return
		}
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

// Degraded reports why the Consul layer is missing; nil when active or not
// configured.
func (r *Runtime[C]) Degraded() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.degraded
}

// Effective reports masked values, provenance and the last rejected update.
func (r *Runtime[C]) Effective() Effective {
	r.mu.Lock()
	rt, rejected := r.rt, r.rejected
	r.mu.Unlock()

	snap := rt.Snapshot()
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
		r.mu.Lock()
		rt := r.rt
		r.mu.Unlock()

		if cerr := rt.Close(); cerr != nil {
			err = fmt.Errorf("config: close: %w", cerr)
		}

		r.wg.Wait()
	})

	return err
}
