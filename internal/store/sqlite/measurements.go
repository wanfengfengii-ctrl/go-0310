package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"thermal-vacuum-test-gate/internal/domain"
)

// AppendMeasurement inserts a point reading.
func (d *DB) AppendMeasurement(ctx context.Context, m domain.Measurement) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO measurements (id, run_id, generation, stage, cycle, sensor_id, temperature_milli_kelvin, pressure_milli_pa, at_millis, valid)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RunID, m.Generation, string(m.Stage), m.Cycle, m.SensorID,
		m.TemperatureMilliKelvin, m.PressureMilliPa, m.AtMillis, boolInt(m.Valid))
	return err
}

// Measurements returns readings for a run/stage/cycle/generation ordered by
// logical time, used for deterministic evidence-window evaluation.
func (d *DB) Measurements(ctx context.Context, runID string, stage domain.StageName, cycle, generation int) ([]domain.Measurement, error) {
	rows, err := d.exec.QueryContext(ctx,
		`SELECT id, run_id, generation, stage, cycle, sensor_id, temperature_milli_kelvin, pressure_milli_pa, at_millis, valid
		 FROM measurements WHERE run_id = ? AND stage = ? AND cycle = ? AND generation = ?
		 ORDER BY at_millis, id`, runID, string(stage), cycle, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Measurement
	for rows.Next() {
		var m domain.Measurement
		var valid int
		if err := rows.Scan(&m.ID, &m.RunID, &m.Generation, &m.Stage, &m.Cycle,
			&m.SensorID, &m.TemperatureMilliKelvin, &m.PressureMilliPa, &m.AtMillis, &valid); err != nil {
			return nil, err
		}
		m.Valid = valid == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// AllMeasurements returns every measurement for a run across all generations,
// ordered deterministically by stage, cycle, sensor and logical time so the
// recovery projection is stable.
func (d *DB) AllMeasurements(ctx context.Context, runID string) ([]domain.Measurement, error) {
	rows, err := d.exec.QueryContext(ctx,
		`SELECT id, run_id, generation, stage, cycle, sensor_id, temperature_milli_kelvin, pressure_milli_pa, at_millis, valid
		 FROM measurements WHERE run_id = ?
		 ORDER BY stage, cycle, sensor_id, at_millis, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Measurement
	for rows.Next() {
		var m domain.Measurement
		var valid int
		if err := rows.Scan(&m.ID, &m.RunID, &m.Generation, &m.Stage, &m.Cycle,
			&m.SensorID, &m.TemperatureMilliKelvin, &m.PressureMilliPa, &m.AtMillis, &valid); err != nil {
			return nil, err
		}
		m.Valid = valid == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// MaxMeasurementTime returns the largest logical time of any measurement for a
// run, used to enforce monotonic time on new readings.
func (d *DB) MaxMeasurementTime(ctx context.Context, runID string) (int64, error) {
	var t sql.NullInt64
	err := d.exec.QueryRowContext(ctx,
		`SELECT MAX(at_millis) FROM measurements WHERE run_id = ?`, runID).Scan(&t)
	if err != nil {
		return 0, err
	}
	if !t.Valid {
		return 0, nil
	}
	return t.Int64, nil
}

// SaveWindow upserts an evidence-window summary.
func (d *DB) SaveWindow(ctx context.Context, w domain.EvidenceWindow) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO evidence_windows (run_id, stage, cycle, coverage_ppm, samples, range_milli_kelvin, drift_ppm, passed)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, stage, cycle) DO UPDATE SET coverage_ppm=excluded.coverage_ppm,
		   samples=excluded.samples, range_milli_kelvin=excluded.range_milli_kelvin,
		   drift_ppm=excluded.drift_ppm, passed=excluded.passed`,
		w.RunID, string(w.Stage), w.Cycle, w.CoveragePPM, w.Samples,
		w.RangeMilliKelvin, w.DriftPPM, boolInt(w.Passed))
	return err
}

// GetWindow loads an evidence-window summary, returning a not-passed zero
// window when none exists yet.
func (d *DB) GetWindow(ctx context.Context, runID string, stage domain.StageName, cycle int) (domain.EvidenceWindow, error) {
	var w domain.EvidenceWindow
	var passed int
	err := d.exec.QueryRowContext(ctx,
		`SELECT run_id, stage, cycle, coverage_ppm, samples, range_milli_kelvin, drift_ppm, passed
		 FROM evidence_windows WHERE run_id = ? AND stage = ? AND cycle = ?`,
		runID, string(stage), cycle).
		Scan(&w.RunID, &w.Stage, &w.Cycle, &w.CoveragePPM, &w.Samples,
			&w.RangeMilliKelvin, &w.DriftPPM, &passed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EvidenceWindow{RunID: runID, Stage: stage, Cycle: cycle}, nil
	}
	if err != nil {
		return domain.EvidenceWindow{}, err
	}
	w.Passed = passed == 1
	return w, nil
}

// AppendCall records a single scripted acquisition attempt.
func (d *DB) AppendCall(ctx context.Context, c domain.MeasurementCall) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO measurement_calls (id, attempt, equipment_id, success, failure_type, payload_summary)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Attempt, c.EquipmentID, boolInt(c.Success), c.FailureType, c.PayloadSummary)
	return err
}

// GetCall loads one acquisition attempt.
func (d *DB) GetCall(ctx context.Context, id string) (domain.MeasurementCall, error) {
	var c domain.MeasurementCall
	var success int
	err := d.exec.QueryRowContext(ctx,
		`SELECT id, attempt, equipment_id, success, failure_type, payload_summary
		 FROM measurement_calls WHERE id = ?`, id).
		Scan(&c.ID, &c.Attempt, &c.EquipmentID, &success, &c.FailureType, &c.PayloadSummary)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MeasurementCall{}, domain.NewError(domain.CodeRunNotFound, "measurement call not found")
	}
	if err != nil {
		return domain.MeasurementCall{}, err
	}
	c.Success = success == 1
	return c, nil
}

// Calls returns all acquisition attempts for an equipment id in attempt order.
func (d *DB) Calls(ctx context.Context, equipmentID string) ([]domain.MeasurementCall, error) {
	rows, err := d.exec.QueryContext(ctx,
		`SELECT id, attempt, equipment_id, success, failure_type, payload_summary
		 FROM measurement_calls WHERE equipment_id = ? ORDER BY attempt`, equipmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MeasurementCall
	for rows.Next() {
		var c domain.MeasurementCall
		var success int
		if err := rows.Scan(&c.ID, &c.Attempt, &c.EquipmentID, &success, &c.FailureType, &c.PayloadSummary); err != nil {
			return nil, err
		}
		c.Success = success == 1
		out = append(out, c)
	}
	return out, rows.Err()
}
