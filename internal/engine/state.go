package engine

import (
	"context"
	"sort"

	"thermal-vacuum-test-gate/internal/domain"
)

// RunState is the recoverable projection of a run: the aggregate, its locked
// plan, baseline, all measurements (deterministically sorted by stage, cycle,
// sensor, then logical time) and the steady-state windows.
type RunState struct {
	Run          domain.TestRun          `json:"run"`
	Plan         domain.TestPlan         `json:"plan"`
	Baseline     domain.Baseline         `json:"baseline"`
	Measurements []domain.Measurement    `json:"measurements"`
	Windows      []domain.EvidenceWindow `json:"windows"`
}

// State builds the run projection. Ordering is stable so a restarted service
// returns an identical representation for the same data.
func (e *Engine) State(ctx context.Context, runID string) (RunState, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	plan, err := e.store.GetPlan(ctx, run.PlanID)
	if err != nil {
		return RunState{}, err
	}
	baseline, err := e.store.GetBaseline(ctx, runID)
	if err != nil {
		baseline = domain.Baseline{RunID: runID}
	}
	all, err := e.store.AllMeasurements(ctx, runID)
	if err != nil {
		return RunState{}, err
	}
	return RunState{Run: run, Plan: plan, Baseline: baseline, Measurements: all, Windows: e.collectWindows(ctx, runID, plan)}, nil
}

// collectWindows reads and returns all saved evidence windows sorted by stage
// then cycle.
func (e *Engine) collectWindows(ctx context.Context, runID string, plan domain.TestPlan) []domain.EvidenceWindow {
	var out []domain.EvidenceWindow
	for _, s := range plan.Stages {
		if !s.Name.IsSoak() {
			continue
		}
		for c := 1; c <= plan.Cycles; c++ {
			w, err := e.store.GetWindow(ctx, runID, s.Name, c)
			if err == nil && w.Samples > 0 {
				out = append(out, w)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stage != out[j].Stage {
			return stageIndex(out[i].Stage) < stageIndex(out[j].Stage)
		}
		return out[i].Cycle < out[j].Cycle
	})
	return out
}

func stageIndex(s domain.StageName) int {
	for i, st := range domain.CanonicalStages {
		if st == s {
			return i
		}
	}
	return len(domain.CanonicalStages)
}
