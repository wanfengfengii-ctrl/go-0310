package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"thermal-vacuum-test-gate/internal/domain"
)

// SavePlan persists a locked plan, serialising sensor and stage slices as
// deterministic JSON.
func (d *DB) SavePlan(ctx context.Context, p domain.TestPlan) error {
	sensors, err := json.Marshal(p.Sensors)
	if err != nil {
		return err
	}
	stages, err := json.Marshal(p.Stages)
	if err != nil {
		return err
	}
	_, err = d.exec.ExecContext(ctx,
		`INSERT INTO plans (id, version, specimen_id, cycles, calibration_summary, locked_at_millis, sensors_json, stages_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET version=excluded.version, specimen_id=excluded.specimen_id,
		   cycles=excluded.cycles, calibration_summary=excluded.calibration_summary,
		   locked_at_millis=excluded.locked_at_millis, sensors_json=excluded.sensors_json,
		   stages_json=excluded.stages_json`,
		p.ID, p.Version, p.SpecimenID, p.Cycles, p.CalibrationSummary,
		p.LockedAtMillis, string(sensors), string(stages))
	return err
}

// GetPlan loads a locked plan by id.
func (d *DB) GetPlan(ctx context.Context, id string) (domain.TestPlan, error) {
	var p domain.TestPlan
	var sensorsJSON, stagesJSON string
	err := d.exec.QueryRowContext(ctx,
		`SELECT id, version, specimen_id, cycles, calibration_summary, locked_at_millis, sensors_json, stages_json
		 FROM plans WHERE id = ?`, id).
		Scan(&p.ID, &p.Version, &p.SpecimenID, &p.Cycles, &p.CalibrationSummary,
			&p.LockedAtMillis, &sensorsJSON, &stagesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TestPlan{}, domain.NewError(domain.CodePlanNotFound, "plan not found")
	}
	if err != nil {
		return domain.TestPlan{}, err
	}
	if err := json.Unmarshal([]byte(sensorsJSON), &p.Sensors); err != nil {
		return domain.TestPlan{}, err
	}
	if err := json.Unmarshal([]byte(stagesJSON), &p.Stages); err != nil {
		return domain.TestPlan{}, err
	}
	return p, nil
}
