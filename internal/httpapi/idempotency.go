package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// canonicalDigest returns a stable digest binding a request path and its body
// bytes. It is the canonical request summary stored with each idempotency key.
func canonicalDigest(path string, body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// idempotencyGate serialises concurrent requests that carry the same
// Idempotency-Key within a single process.
//
// The store writes the idempotency record only after the wrapped operation has
// already executed its side effect, so two identical concurrent requests would
// both run the operation (for POST /v1/runs, both create a run) before either
// record exists, then race on the record insert. The gate makes the second
// caller block until the first has committed its record; the second then
// observes that record on re-check and replays it, so the underlying operation
// runs exactly once per key. The store-level PK on key remains the durable
// backstop for any cross-process races.
type idempotencyGate struct {
	mu   sync.Mutex
	keys map[string]*idempotencyEntry
}

// idempotencyEntry is a per-key mutex with a waiter refcount so the entry can
// be reaped once it goes idle, keeping the keys map from growing without bound.
type idempotencyEntry struct {
	mu   sync.Mutex
	refs int
}

func newIdempotencyGate() *idempotencyGate {
	return &idempotencyGate{keys: make(map[string]*idempotencyEntry)}
}

// acquire locks the per-key mutex and returns a release function. Concurrent
// callers for the same key serialise; the entry is reaped once idle.
func (g *idempotencyGate) acquire(key string) func() {
	g.mu.Lock()
	entry, ok := g.keys[key]
	if !ok {
		entry = &idempotencyEntry{}
		g.keys[key] = entry
	}
	entry.refs++
	g.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		g.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(g.keys, key)
		}
		g.mu.Unlock()
	}
}
