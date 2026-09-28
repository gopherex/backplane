// Package store stands in for a contrib package (contrib/pgx, contrib/valkey):
// a config section plus a deps.Provider built from it. The service embeds the
// section in its Config and passes it to New explicitly.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gopherex/xlog"

	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// ErrScheme is returned for a DSN the store cannot open.
var ErrScheme = errors.New("store: only mem:// is supported")

// Config of one store.
type Config struct {
	// Connection string; never printed.
	DSN config.Secret `json:"dsn" schemapb:"default=mem://"`
	// Requests slower than this are logged; changed live from the console.
	SlowLog config.Live[time.Duration] `json:"slow_log" schemapb:"default=100ms"`
}

// DB counts greetings per name.
type DB struct {
	cfg *Config
	log *xlog.Logger

	mu     sync.Mutex
	counts map[string]uint64
}

// Inc counts a greeting for name and returns the new count.
func (db *DB) Inc(ctx context.Context, name string) uint64 {
	start := time.Now()

	defer func() {
		if took := time.Since(start); took > db.cfg.SlowLog.Get() {
			db.log.Ctx().Warn(ctx, "slow store call", xlog.Duration("took", took))
		}
	}()

	db.mu.Lock()
	defer db.mu.Unlock()

	db.counts[name]++

	return db.counts[name]
}

// Total greetings.
func (db *DB) Total() uint64 {
	db.mu.Lock()
	defer db.mu.Unlock()

	var n uint64
	for _, c := range db.counts {
		n += c
	}

	return n
}

// New is the provider of a store configured by cfg.
func New(cfg *Config) deps.Provider[*DB] { return provider{cfg: cfg} }

type provider struct{ cfg *Config }

func (provider) Name() string { return "store" }

func (p provider) Provide(_ context.Context, s deps.Scope) (*DB, error) {
	if !strings.HasPrefix(p.cfg.DSN.Reveal(), "mem://") {
		return nil, fmt.Errorf("%w: %s", ErrScheme, p.cfg.DSN)
	}

	s.Log().Info("store opened", xlog.String("dsn", p.cfg.DSN.String()))

	return &DB{cfg: p.cfg, log: s.Log(), counts: map[string]uint64{}}, nil
}

func (provider) Probe(context.Context, *DB) error { return nil }

func (provider) Close(context.Context, *DB) error { return nil }
