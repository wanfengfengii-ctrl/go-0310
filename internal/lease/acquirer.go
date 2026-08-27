package lease

import (
	"context"
	"sync"

	"thermal-vacuum-test-gate/internal/domain"
)

// Acquirer is a scripted acquisition adapter. It replays a deterministic queue
// of outcomes per equipment id: each Collect call consumes the next outcome, so
// a caller can script timeout, format error, disconnect, expired calibration
// and finally success in a fixed order. Exhausted scripts repeat their final
// outcome.
type Acquirer struct {
	mu      sync.Mutex
	scripts map[string][]domain.AcquireOutcome
	idx     map[string]int
}

// NewAcquirer builds an empty adapter.
func NewAcquirer() *Acquirer {
	return &Acquirer{scripts: map[string][]domain.AcquireOutcome{}, idx: map[string]int{}}
}

// Script installs a deterministic outcome queue for an equipment id.
func (a *Acquirer) Script(equipmentID string, outcomes ...domain.AcquireOutcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scripts[equipmentID] = outcomes
	a.idx[equipmentID] = 0
}

// Collect returns the next scripted outcome for an equipment id. Without a
// script it reports a successful acquisition.
func (a *Acquirer) Collect(_ context.Context, equipmentID string) domain.AcquireOutcome {
	script, ok := a.scripts[equipmentID]
	if !ok || len(script) == 0 {
		return domain.AcquireOutcome{Success: true}
	}
	i := a.idx[equipmentID]
	if i >= len(script) {
		return script[len(script)-1]
	}
	a.idx[equipmentID] = i + 1
	return script[i]
}
