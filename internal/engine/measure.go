package engine

import (
	"context"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/fixed"
	"thermal-vacuum-test-gate/internal/store"
)

// SubmitMeasurement validates and appends a single point reading. Structural
// violations (wrong stage/cycle/sensor, frozen or completed run) are rejected
// before any write. Lease expiry, stale generation and time regression produce
// a persisted rejection event (so late readings are archived) and return a
// stable domain error.
func (e *Engine) SubmitMeasurement(ctx context.Context, runID string, req domain.MeasurementRequest) (domain.Measurement, error) {
	var out domain.Measurement
	var rejectErr *domain.Error
	err := e.store.WithTx(ctx, func(tx store.Tx) error {
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Completed {
			return domain.NewError(domain.CodeRunCompleted, "run is completed")
		}
		if run.Stage != req.Stage {
			return domain.NewError(domain.CodeStageNotReached, "measurement stage does not match run frontier")
		}
		if req.Stage.IsCycleStage() && req.Cycle != run.CurrentCycle {
			return domain.NewError(domain.CodeStageNotReached, "measurement cycle does not match run cycle")
		}
		plan, err := tx.GetPlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		if !hasSensor(plan, req.SensorID) {
			return domain.NewError(domain.CodeInvalidRange, "unknown sensor")
		}
		if err := checkLease(ctx, tx, req.CollectorID, req.LeaseToken, e.now()); err != nil {
			rejectErr = asDomainErr(err)
			appendRejection(ctx, tx, &run, "measurement.rejected", "expired or invalid lease")
			return nil
		}
		if req.Generation != 0 && req.Generation != run.Generation {
			rejectErr = domain.NewError(domain.CodeInvalidGeneration, "stale generation reading")
			appendRejection(ctx, tx, &run, "measurement.rejected", "stale generation")
			return nil
		}
		if run.Frozen {
			return domain.NewError(domain.CodeRunFrozen, "run is frozen")
		}
		at := req.AtMillis
		if at == 0 {
			at = e.now()
		}
		last, err := tx.MaxMeasurementTime(ctx, runID)
		if err != nil {
			return err
		}
		if at <= last {
			rejectErr = domain.NewError(domain.CodeTimeRegression, "measurement time regressed")
			appendRejection(ctx, tx, &run, "measurement.rejected", "time regression")
			return nil
		}
		m := domain.Measurement{
			ID:                     newID(),
			RunID:                  runID,
			Generation:             run.Generation,
			Stage:                  req.Stage,
			Cycle:                  req.Cycle,
			SensorID:               req.SensorID,
			TemperatureMilliKelvin: req.TemperatureMilliKelvin,
			PressureMilliPa:        req.PressureMilliPa,
			AtMillis:               at,
			Valid:                  true,
		}
		if err := tx.AppendMeasurement(ctx, m); err != nil {
			return err
		}
		run.EventSeq++
		if err := tx.UpdateRun(ctx, run); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.RunEvent{
			Seq:      run.EventSeq,
			RunID:    runID,
			Type:     "measurement.accepted",
			AtMillis: at,
		}); err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return domain.Measurement{}, err
	}
	if rejectErr != nil {
		return domain.Measurement{}, rejectErr
	}
	return out, nil
}

// checkLease verifies that the collector lease is held by the supplied token
// and has not expired. Possession of the token is the authority to record.
func checkLease(ctx context.Context, tx store.DataOps, equipmentID, token string, now int64) error {
	if equipmentID == "" || token == "" {
		return domain.NewError(domain.CodeLeaseExpired, "collector lease required")
	}
	lease, err := tx.GetLease(ctx, equipmentID)
	if err != nil {
		return domain.NewError(domain.CodeLeaseExpired, "no active lease for collector")
	}
	if lease.Token != token {
		return domain.NewError(domain.CodeLeaseExpired, "lease token mismatch")
	}
	if lease.ValidUntilMillis < now {
		return domain.NewError(domain.CodeLeaseExpired, "lease expired")
	}
	return nil
}

// appendRejection archives a late or invalid reading as a rejection event and
// advances the run's event sequence so the archive is monotonic. It never
// mutates the valid measurement index or un-freezes the run.
func appendRejection(ctx context.Context, tx store.Tx, run *domain.TestRun, eventType, reason string) {
	run.EventSeq++
	_ = tx.UpdateRun(ctx, *run)
	_ = tx.AppendEvent(ctx, domain.RunEvent{
		Seq:      run.EventSeq,
		RunID:    run.ID,
		Type:     eventType,
		Payload:  []byte(reason),
		AtMillis: run.CreatedAtMillis,
	})
}

func asDomainErr(err error) *domain.Error {
	if de, ok := err.(*domain.Error); ok {
		return de
	}
	return domain.NewError(domain.CodeInternal, err.Error())
}

func hasSensor(plan domain.TestPlan, id string) bool {
	for _, s := range plan.Sensors {
		if s.ID == id {
			return true
		}
	}
	return false
}

// EvaluateSoak computes the steady-state evidence window for a soak stage from
// the valid measurements of the current generation. Coverage counts distinct
// timestamps per sensor; duplicate timestamps and out-of-window readings never
// contribute to coverage. All fixed-point computations round against a pass.
func EvaluateSoak(plan domain.TestPlan, spec domain.StageSpec, ms []domain.Measurement) (domain.EvidenceWindow, error) {
	w := domain.EvidenceWindow{Stage: spec.Name}
	perSensor := map[string]map[int64]bool{}
	var temps, pressures []int64
	for _, m := range ms {
		if !m.Valid {
			continue
		}
		if perSensor[m.SensorID] == nil {
			perSensor[m.SensorID] = map[int64]bool{}
		}
		perSensor[m.SensorID][m.AtMillis] = true
		temps = append(temps, m.TemperatureMilliKelvin)
		pressures = append(pressures, m.PressureMilliPa)
	}
	coverage := int64(1_000_000)
	var total int64
	for _, s := range plan.Sensors {
		cnt := int64(len(perSensor[s.ID]))
		total += cnt
		cov, err := fixed.Coverage(cnt, spec.RequiredSamples)
		if err != nil {
			return w, err
		}
		if int64(cov) < coverage {
			coverage = int64(cov)
		}
	}
	w.Samples = total
	w.CoveragePPM = coverage
	if len(temps) == 0 {
		return w, nil
	}
	rng, err := fixed.Range(temps...)
	if err != nil {
		return w, err
	}
	w.RangeMilliKelvin = rng
	drift, err := fixed.Drift(minOf(temps), maxOf(temps), fixed.Duration(spec.SoakWindowMillis))
	if err != nil {
		return w, err
	}
	w.DriftPPM = int64(drift)
	maxPressure := maxOf(pressures)
	w.Passed = coverage >= 1_000_000 &&
		rng <= spec.MaxRangeMilliKelvin &&
		w.DriftPPM <= spec.MaxDriftPPM &&
		maxPressure <= spec.MaxPressureMilliPa
	return w, nil
}

func minOf(v []int64) int64 {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func maxOf(v []int64) int64 {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}
