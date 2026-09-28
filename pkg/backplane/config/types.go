package config

import (
	"reflect"
	"sync"
	"sync/atomic"

	sp "github.com/gopherex/schemapb/go/schemapb"
)

// LiveAnnotation marks a live field in the configuration schema: the console
// may change it and Consul KV overrides apply only to such fields.
const LiveAnnotation = "backplane.live"

// Live is a configuration field that changes at runtime: set from the
// console through Consul KV, applied in place. Every holder of the enclosing
// section reads the same current value.
//
// A zero Live (not produced by loading) reads the zero value and never fires.
type Live[T any] struct {
	c *cell[T]
}

type cell[T any] struct {
	value    atomic.Pointer[T]
	mu       sync.Mutex
	watchers []func(T)
	storage  T // decode target; published by value.Store
}

// Get returns the current value.
func (l *Live[T]) Get() T {
	if l.c == nil {
		var zero T

		return zero
	}

	return *l.c.value.Load()
}

// Watch calls fn with every new value applied after loading.
func (l *Live[T]) Watch(fn func(T)) {
	if l.c == nil {
		return
	}

	l.c.mu.Lock()
	defer l.c.mu.Unlock()

	l.c.watchers = append(l.c.watchers, fn)
}

// Replace publishes next's value into this field and notifies watchers when
// it differs. The SDK calls it on reload; application code does not need to.
func (l *Live[T]) Replace(next *Live[T]) {
	if l.c == nil || next.c == nil {
		return
	}

	value := next.c.value.Load()
	if reflect.DeepEqual(*l.c.value.Load(), *value) {
		return
	}

	l.c.value.Store(value)
	l.c.mu.Lock()
	watchers := append([]func(T){}, l.c.watchers...)
	l.c.mu.Unlock()

	for _, fn := range watchers {
		fn(*value)
	}
}

// SchemaInner makes the field's schema the schema of T.
func (*Live[T]) SchemaInner() reflect.Type { return reflect.TypeFor[T]() }

// SchemaField marks the field live.
func (*Live[T]) SchemaField(f *sp.Schema_Field) error {
	if f.Annotations == nil {
		f.Annotations = map[string]*sp.Value{}
	}

	f.Annotations[LiveAnnotation] = sp.BoolV(true)

	return nil
}

// SchemaDecodeTarget gives the decoder a fresh private cell to fill.
func (l *Live[T]) SchemaDecodeTarget() any {
	l.c = &cell[T]{}
	l.c.value.Store(&l.c.storage)

	return &l.c.storage
}

// Secret is a string that never prints: fmt, logs and JSON show "***". The
// schema marks it secret, so the console and the instance state mask it too.
type Secret string

const masked = "***"

// Reveal returns the value.
func (s Secret) Reveal() string { return string(s) }

// String masks the value.
func (Secret) String() string { return masked }

// GoString masks the value.
func (Secret) GoString() string { return masked }

// MarshalJSON masks the value.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + masked + `"`), nil }

// SchemaInner makes the field a plain string in the schema (MarshalJSON
// would otherwise turn it into a JSON field).
func (Secret) SchemaInner() reflect.Type { return reflect.TypeFor[string]() }

// SchemaField marks the field secret.
func (Secret) SchemaField(f *sp.Schema_Field) error {
	f.Secret = true

	return nil
}
