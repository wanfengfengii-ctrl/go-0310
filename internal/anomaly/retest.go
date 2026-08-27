// Package anomaly implements anomaly-driven retest generation and final
// verdict arbitration. It is the "异常复验与终局仲裁器" component.
package anomaly

import (
	"context"
	"sort"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Clock returns the current logical time in milliseconds.
type Clock func() int64

// AffectedClosure computes the deterministic retest set for a failed sensor:
// the anomaly sensor itself, every sensor in the same group, and every sensor
// sharing the same collector. The result is sorted and deduplicated so the
// same fact always yields the same set.
func AffectedClosure(plan domain.TestPlan, anomalySensorID string) []string {
	set := map[string]bool{anomalySensorID: true}
	for _, s := range plan.Sensors {
		if s.ID != anomalySensorID {
			continue
		}
		for _, other := range plan.Sensors {
			sameGroup := s.Group != "" && other.Group == s.Group
			sameCollector := s.CollectorID != "" && other.CollectorID == s.CollectorID
			if other.ID == s.ID || sameGroup || sameCollector {
				set[other.ID] = true
			}
		}
		break
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// retestCoverage returns the sorted steady-state stage names that must be
// re-verified after an anomaly. These are the evidence-bearing soak stages.
func retestCoverage(plan domain.TestPlan) []string {
	set := map[string]bool{}
	for _, s := range plan.Stages {
		if s.Name.IsSoak() {
			set[string(s.Name)] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Generator creates retest generations from anomalies.
type Generator struct {
	store store.Store
	now   Clock
}

// NewGenerator builds a retest generator.
func NewGenerator(s store.Store, now Clock) *Generator {
	return &Generator{store: s, now: now}
}

// CreateAnomaly records an anomaly fact, freezes the current step, computes the
// affected closure and atomically creates the next retest generation. The same
// anomaly fact can only create one generation, and concurrent creators race to
// a single winner via the anomaly primary key.
func (g *Generator) CreateAnomaly(ctx context.Context, runID, sensorID, summary, basis string) (domain.RetestGeneration, error) {
	anomalyID := runID + "/" + sensorID + "/" + basis
	var out domain.RetestGeneration
	err := g.store.WithTx(ctx, func(tx store.Tx) error {
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Completed {
			return domain.NewError(domain.CodeRunCompleted, "run is completed")
		}
		plan, err := tx.GetPlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		if _, err := tx.GetAnomaly(ctx, anomalyID); err == nil {
			return domain.NewError(domain.CodeGenerationConflict, "anomaly fact already recorded")
		}
		a := domain.Anomaly{ID: anomalyID, RunID: runID, Summary: summary, Basis: basis}
		if err := tx.SaveAnomaly(ctx, a); err != nil {
			return domain.NewError(domain.CodeGenerationConflict, "concurrent anomaly creation")
		}
		affected := AffectedClosure(plan, sensorID)
		run.Frozen = true
		run.FreezeReason = summary
		run.Generation++
		run.EventSeq++
		rg := domain.RetestGeneration{
			RunID:      runID,
			Generation: run.Generation,
			Affected:   affected,
			Coverage:   retestCoverage(plan),
		}
		if err := tx.SaveRetestGeneration(ctx, rg); err != nil {
			return err
		}
		if err := tx.UpdateRun(ctx, run); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.RunEvent{
			Seq:      run.EventSeq,
			RunID:    runID,
			Type:     "anomaly.created",
			Payload:  []byte(summary),
			AtMillis: g.now(),
		}); err != nil {
			return err
		}
		out = rg
		return nil
	})
	return out, err
}

// CurrentRetest returns the active retest generation for a run.
func (g *Generator) CurrentRetest(ctx context.Context, runID string) (domain.RetestGeneration, error) {
	return g.store.GetRetestGeneration(ctx, runID)
}
