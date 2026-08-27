package engine

import (
	"context"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Default environmental recovery criteria used when a plan leaves the ambient
// fields at zero.
const (
	DefaultAmbientTempMilliKelvin      = 293_150 // 20 °C
	DefaultAmbientToleranceMilliKelvin = 5_000   // 5 K
	DefaultAmbientPressureMilliPa      = 101_325_000_000
)

// StartStage confirms that a run is at the given frontier stage and is ready
// to receive measurements. It performs no mutation; the real transition and
// its event are committed by CompleteStage.
func (e *Engine) StartStage(ctx context.Context, runID string, stage domain.StageName) (domain.TestRun, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return domain.TestRun{}, err
	}
	if run.Frozen {
		return domain.TestRun{}, domain.NewError(domain.CodeRunFrozen, "run is frozen")
	}
	if run.Completed {
		return domain.TestRun{}, domain.NewError(domain.CodeRunCompleted, "run is completed")
	}
	if run.Stage != stage {
		return domain.TestRun{}, domain.NewError(domain.CodeStageNotReached, "stage is not the current frontier")
	}
	return run, nil
}

// CompleteStage evaluates the pass criteria for the current frontier stage and
// advances the run to the next step in a single transaction. Cycle prefixes
// only ever extend by one from the current completed count, so a run can never
// skip a stage or jump ahead to a later cycle.
func (e *Engine) CompleteStage(ctx context.Context, runID string, stage domain.StageName) (domain.TestRun, error) {
	var out domain.TestRun
	err := e.store.WithTx(ctx, func(tx store.Tx) error {
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Frozen {
			return domain.NewError(domain.CodeRunFrozen, "run is frozen")
		}
		if run.Completed {
			return domain.NewError(domain.CodeRunCompleted, "run is completed")
		}
		if run.Stage != stage {
			return domain.NewError(domain.CodeStageNotReached, "stage is not the current frontier")
		}
		plan, err := tx.GetPlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		spec, ok := stageSpec(plan, stage)
		if !ok {
			return domain.NewError(domain.CodeInvalidStage, "unknown stage in plan")
		}
		cycle := cycleFor(run, stage)
		ms, err := tx.Measurements(ctx, runID, stage, cycle, run.Generation)
		if err != nil {
			return err
		}
		if stage.IsSoak() {
			w, err := EvaluateSoak(plan, spec, ms)
			if err != nil {
				return err
			}
			w.RunID = runID
			w.Cycle = cycle
			if !w.Passed {
				return domain.NewError(domain.CodeInsufficientEvidence, "steady-state window not satisfied")
			}
		} else if err := checkNonSoak(plan, spec, ms); err != nil {
			return err
		}
		if stage == domain.StageHotSoak {
			run.CompletedCycles++
		}
		next, nextCycle, done := advance(stage, run.CurrentCycle, plan.Cycles)
		run.Stage = next
		run.CurrentCycle = nextCycle
		run.Completed = done
		run.EventSeq++
		if err := tx.UpdateRun(ctx, run); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.RunEvent{
			Seq:      run.EventSeq,
			RunID:    runID,
			Type:     "stage.completed",
			Payload:  []byte(string(stage)),
			AtMillis: e.now(),
		}); err != nil {
			return err
		}
		out = run
		return nil
	})
	return out, err
}

// cycleFor returns the cycle number to index measurements for a stage. Cycle
// stages use the run's current cycle; the single-shot stages use cycle 0.
func cycleFor(run domain.TestRun, stage domain.StageName) int {
	if stage.IsCycleStage() {
		return run.CurrentCycle
	}
	return 0
}

// checkNonSoak validates the single-shot stage pass criteria from the valid
// measurements already recorded.
func checkNonSoak(plan domain.TestPlan, spec domain.StageSpec, ms []domain.Measurement) error {
	switch spec.Name {
	case domain.StageEvacuate:
		var best int64
		found := false
		for _, m := range ms {
			if !m.Valid {
				continue
			}
			if !found || m.PressureMilliPa < best {
				best = m.PressureMilliPa
				found = true
			}
		}
		if !found {
			return domain.NewError(domain.CodeInsufficientEvidence, "no pressure evidence for evacuation")
		}
		if best > spec.VacuumTargetMilliPa {
			return domain.NewError(domain.CodeInsufficientEvidence, "vacuum target not reached")
		}
	case domain.StageColdRamp:
		if t, ok := latestTemp(ms); !ok || t > spec.ColdTargetMilliKelvin {
			return domain.NewError(domain.CodeInsufficientEvidence, "cold ramp target not reached")
		}
	case domain.StageHotRamp:
		if t, ok := latestTemp(ms); !ok || t < spec.HotTargetMilliKelvin {
			return domain.NewError(domain.CodeInsufficientEvidence, "hot ramp target not reached")
		}
	case domain.StageReturnAmb:
		t, ok := latestTemp(ms)
		if !ok {
			return domain.NewError(domain.CodeInsufficientEvidence, "no temperature evidence for return to ambient")
		}
		amb, tol := ambient(plan)
		if t < amb-tol || t > amb+tol {
			return domain.NewError(domain.CodeInsufficientEvidence, "environment not returned to ambient")
		}
	case domain.StageRepressurize:
		p, ok := latestPressure(ms)
		if !ok {
			return domain.NewError(domain.CodeInsufficientEvidence, "no pressure evidence for repressurisation")
		}
		if p < ambientPressure(plan) {
			return domain.NewError(domain.CodeInsufficientEvidence, "chamber not safely repressurised")
		}
	default:
		return domain.NewError(domain.CodeInvalidStage, "stage has no completion rule")
	}
	return nil
}

func latestTemp(ms []domain.Measurement) (int64, bool) {
	var last domain.Measurement
	found := false
	for _, m := range ms {
		if !m.Valid {
			continue
		}
		if !found || m.AtMillis > last.AtMillis {
			last = m
			found = true
		}
	}
	return last.TemperatureMilliKelvin, found
}

func latestPressure(ms []domain.Measurement) (int64, bool) {
	var last domain.Measurement
	found := false
	for _, m := range ms {
		if !m.Valid {
			continue
		}
		if !found || m.AtMillis > last.AtMillis {
			last = m
			found = true
		}
	}
	return last.PressureMilliPa, found
}

func ambient(plan domain.TestPlan) (int64, int64) {
	temp := plan.AmbientTempMilliKelvin
	if temp == 0 {
		temp = DefaultAmbientTempMilliKelvin
	}
	tol := plan.AmbientToleranceMilliKelvin
	if tol == 0 {
		tol = DefaultAmbientToleranceMilliKelvin
	}
	return temp, tol
}

func ambientPressure(plan domain.TestPlan) int64 {
	if plan.AmbientPressureMilliPa == 0 {
		return DefaultAmbientPressureMilliPa
	}
	return plan.AmbientPressureMilliPa
}
