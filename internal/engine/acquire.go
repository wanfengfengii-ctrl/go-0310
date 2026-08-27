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
// Determining the attempt number and archiving the call run inside a single
// store transaction: the call history read, the attempt computation and the
// call append are committed together. Because the SQLite store is a single
// serialized connection, the active transaction holds it for the duration, so
// two concurrent collects against the same collector can never both observe an
// identical call history and therefore can never share an attempt number. The
// scripted outcome is consumed only after the attempt number is fixed, keeping
// the adapter's replay order identical to the persisted attempt order.
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
