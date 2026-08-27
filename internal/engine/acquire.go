package engine

import (
	"context"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

// Acquirer produces a scripted acquisition outcome for an equipment id. It is
// the seam the engine uses to drive the deterministic equipment adapter.
type Acquirer interface {
	Collect(ctx context.Context, equipmentID string) domain.AcquireOutcome
}

// SetAcquirer wires the scripted acquisition adapter into the engine. It is
// optional: without it, CollectMeasurement still records deterministic call
// attempts using the readings supplied by the caller.
func (e *Engine) SetAcquirer(a Acquirer) { e.acquirer = a }

// CollectMeasurement drives one scripted acquisition attempt against a
// collector: it determines the deterministic attempt number from the persisted
// call history, asks the adapter for an outcome, records the call, and only on
// success submits the reading through the standard measurement path. Failed
// attempts are archived as calls and never produce a valid measurement.
//
// The call-history read and the call append run in one transaction so that the
// 1-based attempt number is assigned atomically: concurrent /collect requests
// against the same collector cannot both observe the same call count and
// therefore cannot collide on an attempt number or skip a position. The
// scripted outcome is consumed under the same critical section (the adapter
// advances its own per-collector index under a mutex) so each persisted attempt
// is paired with exactly one scripted outcome in the preset order.
func (e *Engine) CollectMeasurement(ctx context.Context, runID string, req domain.MeasurementRequest) (domain.MeasurementCall, domain.Measurement, error) {
	var call domain.MeasurementCall
	var outcome domain.AcquireOutcome
	err := e.store.WithTx(ctx, func(tx store.Tx) error {
		calls, err := tx.Calls(ctx, req.CollectorID)
		if err != nil {
			return err
		}
		attempt := len(calls) + 1
		if e.acquirer != nil {
			outcome = e.acquirer.Collect(ctx, req.CollectorID)
		} else {
			outcome = domain.AcquireOutcome{
				Success:                true,
				TemperatureMilliKelvin: req.TemperatureMilliKelvin,
				PressureMilliPa:        req.PressureMilliPa,
			}
		}
		call = domain.MeasurementCall{
			ID:             newID(),
			Attempt:        attempt,
			EquipmentID:    req.CollectorID,
			Success:        outcome.Success,
			FailureType:    outcome.FailureType,
			PayloadSummary: outcome.PayloadSummary,
		}
		return tx.AppendCall(ctx, call)
	})
	if err != nil {
		return domain.MeasurementCall{}, domain.Measurement{}, err
	}
	if !outcome.Success {
		return call, domain.Measurement{}, domain.NewError(domain.CodeConflict, "acquisition failed: "+outcome.FailureType)
	}
	req.TemperatureMilliKelvin = outcome.TemperatureMilliKelvin
	req.PressureMilliPa = outcome.PressureMilliPa
	m, err := e.SubmitMeasurement(ctx, runID, req)
	return call, m, err
}
