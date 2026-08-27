package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"thermal-vacuum-test-gate/internal/domain"
)

func runScan(scanner interface{ Scan(...any) error }) (domain.TestRun, error) {
	var r domain.TestRun
	var baselineDone, frozen, completed int
	err := scanner.Scan(&r.ID, &r.PlanID, &r.PlanVersion, &r.Generation, &r.Stage,
		&r.CurrentCycle, &r.CompletedCycles, &baselineDone, &frozen,
		&r.FreezeReason, &completed, &r.EventSeq, &r.CreatedAtMillis)
	if err != nil {
		return domain.TestRun{}, err
	}
	r.BaselineDone = baselineDone == 1
	r.Frozen = frozen == 1
	r.Completed = completed == 1
	return r, nil
}

const runColumns = `id, plan_id, plan_version, generation, stage, current_cycle,
	completed_cycles, baseline_done, frozen, freeze_reason, completed, event_seq, created_at_millis`

// CreateRun inserts a new run aggregate.
func (d *DB) CreateRun(ctx context.Context, r domain.TestRun) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO runs (`+runColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.PlanID, r.PlanVersion, r.Generation, string(r.Stage),
		r.CurrentCycle, r.CompletedCycles, boolInt(r.BaselineDone), boolInt(r.Frozen),
		r.FreezeReason, boolInt(r.Completed), r.EventSeq, r.CreatedAtMillis)
	return err
}

// GetRun loads a run by id.
func (d *DB) GetRun(ctx context.Context, id string) (domain.TestRun, error) {
	r, err := runScan(d.exec.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TestRun{}, domain.NewError(domain.CodeRunNotFound, "run not found")
	}
	return r, err
}

// ListRuns returns all runs ordered by creation time for recovery projection.
func (d *DB) ListRuns(ctx context.Context) ([]domain.TestRun, error) {
	rows, err := d.exec.QueryContext(ctx,
		`SELECT `+runColumns+` FROM runs ORDER BY created_at_millis, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TestRun
	for rows.Next() {
		r, err := runScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRun overwrites the authoritative run aggregate state.
func (d *DB) UpdateRun(ctx context.Context, r domain.TestRun) error {
	res, err := d.exec.ExecContext(ctx,
		`UPDATE runs SET plan_id=?, plan_version=?, generation=?, stage=?, current_cycle=?,
		 completed_cycles=?, baseline_done=?, frozen=?, freeze_reason=?, completed=?, event_seq=?, created_at_millis=?
		 WHERE id=?`,
		r.PlanID, r.PlanVersion, r.Generation, string(r.Stage), r.CurrentCycle,
		r.CompletedCycles, boolInt(r.BaselineDone), boolInt(r.Frozen),
		r.FreezeReason, boolInt(r.Completed), r.EventSeq, r.CreatedAtMillis, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.NewError(domain.CodeRunNotFound, "run not found")
	}
	return nil
}

// AppendEvent appends an append-only event. The (run_id, seq) primary key
// enforces monotonic sequence numbers per run.
func (d *DB) AppendEvent(ctx context.Context, e domain.RunEvent) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO run_events (seq, run_id, type, payload, at_millis) VALUES (?, ?, ?, ?, ?)`,
		e.Seq, e.RunID, e.Type, e.Payload, e.AtMillis)
	return err
}

// Events returns events for a run with seq strictly greater than afterSeq,
// ordered by seq for deterministic replay.
func (d *DB) Events(ctx context.Context, runID string, afterSeq int64) ([]domain.RunEvent, error) {
	rows, err := d.exec.QueryContext(ctx,
		`SELECT seq, run_id, type, payload, at_millis FROM run_events
		 WHERE run_id = ? AND seq > ? ORDER BY seq`, runID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RunEvent
	for rows.Next() {
		var e domain.RunEvent
		if err := rows.Scan(&e.Seq, &e.RunID, &e.Type, &e.Payload, &e.AtMillis); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveBaseline upserts the baseline evidence for a run.
func (d *DB) SaveBaseline(ctx context.Context, b domain.Baseline) error {
	zeros, err := json.Marshal(b.SensorZeros)
	if err != nil {
		return err
	}
	_, err = d.exec.ExecContext(ctx,
		`INSERT INTO baselines (run_id, install_check_ok, door_closed, initial_pressure_milli_pa, sensor_zeros_json, completed, completed_at_millis)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET install_check_ok=excluded.install_check_ok,
		   door_closed=excluded.door_closed, initial_pressure_milli_pa=excluded.initial_pressure_milli_pa,
		   sensor_zeros_json=excluded.sensor_zeros_json, completed=excluded.completed,
		   completed_at_millis=excluded.completed_at_millis`,
		b.RunID, boolInt(b.InstallCheckOK), boolInt(b.DoorClosed),
		b.InitialPressureMilliPa, string(zeros), boolInt(b.Completed), b.CompletedAtMillis)
	if err != nil {
		return err
	}
	return nil
}

// GetBaseline loads the baseline evidence for a run.
func (d *DB) GetBaseline(ctx context.Context, runID string) (domain.Baseline, error) {
	var b domain.Baseline
	var zerosJSON string
	var installCheckOK, doorClosed, completed int
	err := d.exec.QueryRowContext(ctx,
		`SELECT run_id, install_check_ok, door_closed, initial_pressure_milli_pa, sensor_zeros_json, completed, completed_at_millis
		 FROM baselines WHERE run_id = ?`, runID).
		Scan(&b.RunID, &installCheckOK, &doorClosed, &b.InitialPressureMilliPa,
			&zerosJSON, &completed, &b.CompletedAtMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Baseline{}, domain.NewError(domain.CodeBaselineMissing, "baseline not recorded")
	}
	if err != nil {
		return domain.Baseline{}, err
	}
	b.InstallCheckOK = installCheckOK == 1
	b.DoorClosed = doorClosed == 1
	b.Completed = completed == 1
	if err := json.Unmarshal([]byte(zerosJSON), &b.SensorZeros); err != nil {
		return domain.Baseline{}, err
	}
	return b, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
