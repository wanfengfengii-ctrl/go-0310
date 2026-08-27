package engine

import (
	"context"
	"sort"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// SubmitBaseline validates and completes the pre-flight baseline (install
// check, sensor zero readings, door closed and initial pressure). Evacuation
// may not begin until every item passes.
func (e *Engine) SubmitBaseline(ctx context.Context, runID string, req domain.BaselineRequest) (domain.Baseline, error) {
	var out domain.Baseline
	err := e.store.WithTx(ctx, func(tx store.Tx) error {
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Frozen {
			return domain.NewError(domain.CodeRunFrozen, "run is frozen")
		}
		if run.BaselineDone {
			return domain.NewError(domain.CodeInvalidStage, "baseline already completed")
		}
		plan, err := tx.GetPlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		if err := validateBaseline(plan, req); err != nil {
			return err
		}
		b := domain.Baseline{
			RunID:                  runID,
			InstallCheckOK:         req.InstallCheckOK,
			DoorClosed:             req.DoorClosed,
			InitialPressureMilliPa: req.InitialPressureMilliPa,
			SensorZeros:            req.SensorZeros,
			Completed:              true,
			CompletedAtMillis:      e.now(),
		}
		if err := tx.SaveBaseline(ctx, b); err != nil {
			return err
		}
		run.BaselineDone = true
		run.Stage = domain.StageEvacuate
		run.EventSeq++
		if err := tx.UpdateRun(ctx, run); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.RunEvent{
			Seq:      run.EventSeq,
			RunID:    runID,
			Type:     "baseline.completed",
			AtMillis: b.CompletedAtMillis,
		}); err != nil {
			return err
		}
		out = b
		return nil
	})
	return out, err
}

// validateBaseline checks the install check, door closure, sensor zero coverage
// and initial pressure. Reasons are returned in deterministic order.
func validateBaseline(plan domain.TestPlan, req domain.BaselineRequest) error {
	var reasons []string
	if !req.InstallCheckOK {
		reasons = append(reasons, "install check did not pass")
	}
	if !req.DoorClosed {
		reasons = append(reasons, "chamber door is not closed")
	}
	if req.InitialPressureMilliPa < 0 {
		reasons = append(reasons, "initial pressure must be non-negative")
	}
	var missing []string
	for _, s := range plan.Sensors {
		if _, ok := req.SensorZeros[s.ID]; !ok {
			missing = append(missing, "missing sensor zero: "+s.ID)
		}
	}
	sort.Strings(missing)
	reasons = append(reasons, missing...)
	if len(reasons) > 0 {
		return domain.NewError(domain.CodeBaselineInvalid, "baseline validation failed").WithReasons(reasons...)
	}
	return nil
}
