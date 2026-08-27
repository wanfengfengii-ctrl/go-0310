// Package engine orchestrates the run aggregate: baseline completion, stage
// progression, cycle prefixes, measurement ingestion and steady-state evidence
// evaluation. It is the "工况与测量引擎" component of the specification.
//
// Every state transition runs inside a single store transaction so that stage
// advancement, evidence indexing, lease changes and event appends commit or
// roll back together (事务边界).
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Clock returns the current logical time in milliseconds.
type Clock func() int64

// Engine drives the run lifecycle against a store.
type Engine struct {
	store    store.Store
	now      Clock
	acquirer Acquirer
}

// New builds an Engine. now may be nil, in which case a wall-clock-agnostic
// monotonic fallback is used (the store's persisted logical time is preferred
// by callers that need strict monotonicity).
func New(s store.Store, now Clock) *Engine {
	if now == nil {
		now = wallClock
	}
	return &Engine{store: s, now: now}
}

// Store exposes the underlying store for recovery and HTTP wiring.
func (e *Engine) Store() store.Store { return e.store }

// Now returns the current logical time.
func (e *Engine) Now() int64 { return e.now() }

// CreateRun creates a run aggregate bound to a locked plan. The run starts at
// the baseline stage with a single generation.
func (e *Engine) CreateRun(ctx context.Context, planID, runID string) (domain.TestRun, error) {
	plan, err := e.store.GetPlan(ctx, planID)
	if err != nil {
		return domain.TestRun{}, err
	}
	run := domain.TestRun{
		ID:              runID,
		PlanID:          plan.ID,
		PlanVersion:     plan.Version,
		Generation:      1,
		Stage:           domain.StageBaseline,
		CurrentCycle:    0,
		CompletedCycles: 0,
		EventSeq:        0,
		CreatedAtMillis: e.now(),
	}
	run.EventSeq = 1
	err = e.store.WithTx(ctx, func(tx store.Tx) error {
		if err := tx.CreateRun(ctx, run); err != nil {
			return err
		}
		return tx.AppendEvent(ctx, domain.RunEvent{
			Seq:      1,
			RunID:    run.ID,
			Type:     "run.created",
			AtMillis: run.CreatedAtMillis,
		})
	})
	if err != nil {
		return domain.TestRun{}, err
	}
	return run, nil
}

// GetRun loads the current run aggregate.
func (e *Engine) GetRun(ctx context.Context, runID string) (domain.TestRun, error) {
	return e.store.GetRun(ctx, runID)
}

// advance returns the next stage/cycle after completing the given step, or
// done=true when the whole workflow (all cycles plus recovery) is finished.
// It encodes the fixed workflow order from the specification.
func advance(stage domain.StageName, cycle, cycles int) (domain.StageName, int, bool) {
	switch stage {
	case domain.StageBaseline:
		return domain.StageEvacuate, 0, false
	case domain.StageEvacuate:
		return domain.StageColdRamp, 1, false
	case domain.StageColdRamp:
		return domain.StageColdSoak, cycle, false
	case domain.StageColdSoak:
		return domain.StageHotRamp, cycle, false
	case domain.StageHotRamp:
		return domain.StageHotSoak, cycle, false
	case domain.StageHotSoak:
		if cycle < cycles {
			return domain.StageColdRamp, cycle + 1, false
		}
		return domain.StageReturnAmb, 0, false
	case domain.StageReturnAmb:
		return domain.StageRepressurize, 0, false
	case domain.StageRepressurize:
		return domain.StageBaseline, 0, true
	default:
		return domain.StageBaseline, 0, true
	}
}

// stageSpec finds the plan's stage spec by name.
func stageSpec(plan domain.TestPlan, name domain.StageName) (domain.StageSpec, bool) {
	for _, s := range plan.Stages {
		if s.Name == name {
			return s, true
		}
	}
	return domain.StageSpec{}, false
}

// newID returns a random hex identifier for events and records.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// wallClock is a millisecond wall clock used when no logical clock is wired.
func wallClock() int64 {
	return timeNowMillis()
}
