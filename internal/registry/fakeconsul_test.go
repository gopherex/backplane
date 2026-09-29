package registry_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"
)

// fakeConsul serves the three blocking queries the registry makes, with one
// index for everything: a query at the current index blocks until a change
// or its wait ends.
type fakeConsul struct {
	srv *httptest.Server

	mu      sync.Mutex
	index   uint64
	changed chan struct{} // closed and replaced on every change
	down    bool
	kv      map[string][]byte
	modify  map[string]uint64
	health  map[string][]*api.ServiceEntry
}

func newFakeConsul(t *testing.T) *fakeConsul {
	t.Helper()

	f := &fakeConsul{
		index: 1, changed: make(chan struct{}),
		kv: map[string][]byte{}, modify: map[string]uint64{}, health: map[string][]*api.ServiceEntry{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/catalog/services", f.serve(f.services))
	mux.HandleFunc("GET /v1/kv/", f.serve(f.list))
	mux.HandleFunc("GET /v1/health/service/{name}", f.serve(f.serviceHealth))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeConsul) client(t *testing.T) *api.Client {
	t.Helper()

	c, err := api.NewClient(&api.Config{Address: strings.TrimPrefix(f.srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// serve answers with body() at the current index, blocking first when
// the request waits on it; body reports false for 404.
func (f *fakeConsul) serve(body func(r *http.Request) (any, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wait, _ := time.ParseDuration(r.URL.Query().Get("wait"))
		if wait <= 0 {
			wait = time.Minute
		}

		index, _ := strconv.ParseUint(r.URL.Query().Get("index"), 10, 64)

		f.mu.Lock()
		if index > 0 && index >= f.index {
			ch := f.changed
			f.mu.Unlock()

			select {
			case <-ch:
			case <-time.After(wait):
			case <-r.Context().Done():
				return
			}

			f.mu.Lock()
		}
		defer f.mu.Unlock()

		if f.down {
			http.Error(w, "down", http.StatusInternalServerError)

			return
		}

		w.Header().Set("X-Consul-Index", strconv.FormatUint(f.index, 10))

		v, ok := body(r)
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_ = json.NewEncoder(w).Encode(v)
	}
}

// services: every service with a registration; f.mu is held.
func (f *fakeConsul) services(*http.Request) (any, bool) {
	out := map[string][]string{"consul": {}}

	for name, entries := range f.health {
		if len(entries) > 0 {
			out[name] = []string{}
		}
	}

	return out, true
}

func (f *fakeConsul) list(r *http.Request) (any, bool) {
	prefix := strings.TrimPrefix(r.URL.Path, "/v1/kv/")

	var out api.KVPairs

	for k, v := range f.kv {
		if strings.HasPrefix(k, prefix) {
			out = append(out, &api.KVPair{Key: k, Value: v, ModifyIndex: f.modify[k]})
		}
	}

	slices.SortFunc(out, func(a, b *api.KVPair) int { return strings.Compare(a.Key, b.Key) })

	return out, len(out) > 0
}

func (f *fakeConsul) serviceHealth(r *http.Request) (any, bool) {
	entries := f.health[r.PathValue("name")]
	if entries == nil {
		entries = []*api.ServiceEntry{}
	}

	return entries, true
}

// change applies fn and wakes the blocked queries.
func (f *fakeConsul) change(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn()

	f.index++
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeConsul) put(t *testing.T, key string, m proto.Message) {
	t.Helper()

	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	f.putRaw(key, b)
}

func (f *fakeConsul) putRaw(key string, b []byte) {
	f.change(func() {
		f.kv[key] = b
		f.modify[key] = f.index + 1
	})
}

func (f *fakeConsul) del(key string) {
	f.change(func() {
		delete(f.kv, key)
		delete(f.modify, key)
	})
}

// register sets the health entries of a service.
func (f *fakeConsul) register(name string, entries ...*api.ServiceEntry) {
	f.change(func() { f.health[name] = entries })
}

func (f *fakeConsul) setDown(down bool) {
	f.change(func() { f.down = down })
}

// entry is a registration of service name with one check in status.
func entry(name, id, addr string, port int, status string, tags ...string) *api.ServiceEntry {
	return &api.ServiceEntry{
		Node:    &api.Node{Node: "node-1", Address: "10.0.0.1"},
		Service: &api.AgentService{ID: id, Service: name, Address: addr, Port: port, Tags: tags},
		Checks:  api.HealthChecks{{CheckID: "grpc", Status: status}},
	}
}
