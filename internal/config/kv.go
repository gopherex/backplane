package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/consul/api"
)

// Consul KV layout (§5.3, §17).
const (
	// Root of every service's overrides.
	Root = "config/"
	// RevisionKey, under config/<service>/, is the revision of the values
	// next to it (the SDK reads it with them and never applies it).
	RevisionKey = "_revision"
	// maxTxnOps is Consul's limit of operations in one transaction.
	maxTxnOps = 64
	// lastReserved are the operations of the last transaction besides the
	// values: the CAS check and _revision.
	lastReserved = 2
)

// Reasons KV is rewritten.
const (
	reasonMissing = "missing" // nothing delivered yet, or wiped
	reasonStale   = "stale"   // another revision
	reasonEdited  = "edited"  // the revision matches, the values do not
)

// errConflict: KV changed between reading and writing it.
var errConflict = errors.New("config: consul kv changed concurrently")

// prefix of a service's keys.
func prefix(service string) string { return Root + service + "/" }

// target is what Consul KV must hold for a service: the values of its
// current revision.
type target struct {
	service  string
	revision int64
	// kv: Live path -> encoded value.
	kv map[string]string
}

func (t target) key(path string) string { return prefix(t.service) + kvPath(path) }

func (t target) revisionKey() string { return prefix(t.service) + RevisionKey }

// values are the keys and values KV must hold, _revision aside.
func (t target) values() map[string]string {
	out := make(map[string]string, len(t.kv))
	for path, v := range t.kv {
		out[t.key(path)] = v
	}

	return out
}

// pairsOf keeps the pairs of one service by key; folder markers (keys
// ending in / with no value, made by the Consul UI) are not values: the
// SDK ignores them and so does the comparison.
func pairsOf(service string, all api.KVPairs) map[string]*api.KVPair {
	out := map[string]*api.KVPair{}
	p := prefix(service)

	for _, kv := range all {
		if kv == nil || !strings.HasPrefix(kv.Key, p) || (strings.HasSuffix(kv.Key, "/") && len(kv.Value) == 0) {
			continue
		}

		out[kv.Key] = kv
	}

	return out
}

// drift is why pairs differ from t ("" when they match) and the keys that
// differ, _revision aside.
func (t target) drift(pairs map[string]*api.KVPair) (string, []string) {
	want := t.values()

	var edited []string

	for key, v := range want {
		if p := pairs[key]; p == nil || string(p.Value) != v {
			edited = append(edited, key)
		}
	}

	for key := range pairs {
		if _, ok := want[key]; !ok && key != t.revisionKey() {
			edited = append(edited, key)
		}
	}

	slices.Sort(edited)

	rev := pairs[t.revisionKey()]

	switch {
	case rev == nil && len(pairs) == 0:
		return reasonMissing, edited
	case rev == nil || strings.TrimSpace(string(rev.Value)) != strconv.FormatInt(t.revision, 10):
		return reasonStale, edited
	case len(edited) > 0:
		return reasonEdited, edited
	}

	return "", nil
}

// txns are the transactions that take pairs to t: changed values first,
// then deletions of keys no longer overridden, then _revision — last, in
// the last transaction, which also checks that _revision is still what
// pairs saw (CAS: a replica holding an older revision never overwrites a
// newer one). Up to 62 changes make one atomic transaction; more are split
// into transactions of 64, and until the last one lands a reader may see
// part of the new values under the old _revision.
func (t target) txns(pairs map[string]*api.KVPair) []api.TxnOps {
	var ops api.TxnOps

	want := t.values()
	for _, key := range slices.Sorted(maps.Keys(want)) {
		if p := pairs[key]; p == nil || string(p.Value) != want[key] {
			ops = append(ops, &api.TxnOp{KV: &api.KVTxnOp{Verb: api.KVSet, Key: key, Value: []byte(want[key])}})
		}
	}

	for _, key := range slices.Sorted(maps.Keys(pairs)) {
		if _, ok := want[key]; !ok && key != t.revisionKey() {
			ops = append(ops, &api.TxnOp{KV: &api.KVTxnOp{Verb: api.KVDelete, Key: key}})
		}
	}

	check := &api.KVTxnOp{Verb: api.KVCheckNotExists, Key: t.revisionKey()}
	if p := pairs[t.revisionKey()]; p != nil {
		check = &api.KVTxnOp{Verb: api.KVCheckIndex, Key: t.revisionKey(), Index: p.ModifyIndex}
	}

	revision := &api.TxnOp{KV: &api.KVTxnOp{
		Verb: api.KVSet, Key: t.revisionKey(), Value: []byte(strconv.FormatInt(t.revision, 10)),
	}}

	var out []api.TxnOps

	for len(ops) > maxTxnOps-lastReserved {
		n := min(len(ops)-(maxTxnOps-lastReserved), maxTxnOps)
		out = append(out, ops[:n])
		ops = ops[n:]
	}

	last := append(api.TxnOps{{KV: check}}, ops...)

	return append(out, append(last, revision))
}

// write applies txns; a failed CAS is errConflict.
func write(ctx context.Context, client *api.Client, txns []api.TxnOps) error {
	for i, ops := range txns {
		ok, resp, _, err := client.Txn().Txn(ops, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			return fmt.Errorf("config: consul txn: %w", err)
		}

		if ok {
			continue
		}

		var msgs []string

		for _, e := range resp.Errors {
			if i == len(txns)-1 && e.OpIndex == 0 {
				return errConflict
			}

			msgs = append(msgs, e.What)
		}

		return fmt.Errorf("config: consul txn rolled back: %s", strings.Join(msgs, "; ")) //nolint:err113 // Consul's reasons
	}

	return nil
}
