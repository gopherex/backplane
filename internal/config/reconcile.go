package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xlog"
)

const (
	watchMinDelay = time.Second
	watchMaxDelay = 30 * time.Second
	watchGrowth   = 2
)

// reconcileLoop runs a pass at start, every ReconcileInterval and on every
// Kick (a change under config/, a lost CAS) until the node stops.
//
// Replicas run it side by side: every write is conditional on the
// _revision it read, and they all write what PostgreSQL holds, so the
// passes converge instead of fighting.
func (m *Manager) reconcileLoop(ctx context.Context) error {
	tick := time.NewTicker(m.settings.ReconcileInterval)
	defer tick.Stop()

	for {
		m.pass(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-m.kick:
		}
	}
}

// pass reconciles once; its failures are logged when they start and end.
func (m *Manager) pass(ctx context.Context) {
	err := m.reconcile(ctx)
	if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	was := m.failing
	m.failing = err
	m.mu.Unlock()

	switch {
	case err != nil && was == nil:
		m.metrics.failure(ctx)
		m.Log().Warn("config reconcile failed: retrying", xlog.Err(err))
	case err != nil:
		m.metrics.failure(ctx)
	case was != nil:
		m.Log().Info("config reconcile recovered")
	}

	// Revisions saved by other replicas reach this one's watchers here.
	m.changes.notify()
}

// reconcile compares config/ (one query) with every service's current
// revision (one query) and syncs the services that differ.
func (m *Manager) reconcile(ctx context.Context) error {
	all, _, err := m.consul.KV().List(Root, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return fmt.Errorf("config: consul kv %s: %w", Root, err)
	}

	rows, err := m.store.Get().Q.ListCurrentConfigKv(ctx)
	if err != nil {
		return fmt.Errorf("config: current revisions: %w", err)
	}

	var errs []error

	for _, r := range rows {
		t := target{service: r.Service, revision: r.Revision}
		if err := json.Unmarshal(r.Kv, &t.kv); err != nil {
			errs = append(errs, fmt.Errorf("config: revision %s/%d: kv: %w", r.Service, r.Revision, err))

			continue
		}

		if reason, _ := t.drift(pairsOf(r.Service, all)); reason == "" {
			continue
		}

		if err := m.Sync(ctx, r.Service); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Service, err))
		}
	}

	return errors.Join(errs...)
}

// watchKV follows config/ with a blocking query and kicks the reconciler
// on every change — a save, a manual edit, a wipe.
func (m *Manager) watchKV(ctx context.Context) error {
	var (
		index   uint64
		started bool
		delay   = watchMinDelay
	)

	for ctx.Err() == nil {
		q := (&api.QueryOptions{WaitIndex: index, WaitTime: m.wait}).WithContext(ctx)

		_, meta, err := m.consul.KV().Keys(Root, "", q)
		if err != nil {
			// Consul is down or ctx ended: back off, the loop condition
			// ends the watch.
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}

			delay = min(watchGrowth*delay, watchMaxDelay)

			continue
		}

		delay = watchMinDelay

		if started && meta.LastIndex != index {
			m.Kick()
		}

		started = true

		// A lower index: Consul's index was reset (a snapshot restore).
		if meta.LastIndex < index || meta.LastIndex == 0 {
			index = 0
		} else {
			index = meta.LastIndex
		}
	}

	return nil
}
