package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"thermal-vacuum-test-gate/internal/domain"
)

// SaveAnomaly inserts an anomaly fact.
func (d *DB) SaveAnomaly(ctx context.Context, a domain.Anomaly) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO anomalies (id, run_id, summary, basis) VALUES (?, ?, ?, ?)`,
		a.ID, a.RunID, a.Summary, a.Basis)
	return err
}

// GetAnomaly loads an anomaly fact by id.
func (d *DB) GetAnomaly(ctx context.Context, id string) (domain.Anomaly, error) {
	var a domain.Anomaly
	err := d.exec.QueryRowContext(ctx,
		`SELECT id, run_id, summary, basis FROM anomalies WHERE id = ?`, id).
		Scan(&a.ID, &a.RunID, &a.Summary, &a.Basis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Anomaly{}, domain.NewError(domain.CodeRunNotFound, "anomaly not found")
	}
	if err != nil {
		return domain.Anomaly{}, err
	}
	return a, nil
}

// SaveRetestGeneration upserts the retest generation for a run. The primary
// key on run_id means only one active generation exists; a conflict signals a
// concurrent creator.
func (d *DB) SaveRetestGeneration(ctx context.Context, rg domain.RetestGeneration) error {
	affected, err := json.Marshal(rg.Affected)
	if err != nil {
		return err
	}
	coverage, err := json.Marshal(rg.Coverage)
	if err != nil {
		return err
	}
	_, err = d.exec.ExecContext(ctx,
		`INSERT INTO retest_generations (run_id, generation, affected_json, coverage_json) VALUES (?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET generation=excluded.generation,
		   affected_json=excluded.affected_json, coverage_json=excluded.coverage_json`,
		rg.RunID, rg.Generation, string(affected), string(coverage))
	return err
}

// GetRetestGeneration loads the active retest generation for a run.
func (d *DB) GetRetestGeneration(ctx context.Context, runID string) (domain.RetestGeneration, error) {
	var rg domain.RetestGeneration
	var affectedJSON, coverageJSON string
	err := d.exec.QueryRowContext(ctx,
		`SELECT run_id, generation, affected_json, coverage_json FROM retest_generations WHERE run_id = ?`,
		runID).Scan(&rg.RunID, &rg.Generation, &affectedJSON, &coverageJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RetestGeneration{}, domain.NewError(domain.CodeRunNotFound, "retest generation not found")
	}
	if err != nil {
		return domain.RetestGeneration{}, err
	}
	if err := json.Unmarshal([]byte(affectedJSON), &rg.Affected); err != nil {
		return domain.RetestGeneration{}, err
	}
	if err := json.Unmarshal([]byte(coverageJSON), &rg.Coverage); err != nil {
		return domain.RetestGeneration{}, err
	}
	return rg, nil
}

// AddReview inserts a review. The UNIQUE(run_id, reviewer) constraint makes a
// duplicate reviewer review fail atomically.
func (d *DB) AddReview(ctx context.Context, r domain.Review) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO reviews (id, run_id, reviewer, qualified, digest) VALUES (?, ?, ?, ?, ?)`,
		r.ID, r.RunID, r.Reviewer, boolInt(r.Qualified), r.Digest)
	return err
}

// Reviews returns reviews for a run ordered by reviewer for determinism.
func (d *DB) Reviews(ctx context.Context, runID string) ([]domain.Review, error) {
	rows, err := d.exec.QueryContext(ctx,
		`SELECT id, run_id, reviewer, qualified, digest FROM reviews WHERE run_id = ? ORDER BY reviewer`,
		runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Review
	for rows.Next() {
		var r domain.Review
		var qualified int
		if err := rows.Scan(&r.ID, &r.RunID, &r.Reviewer, &qualified, &r.Digest); err != nil {
			return nil, err
		}
		r.Qualified = qualified == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// CommitVerdict inserts the unique final verdict. The primary key on run_id
// means only the first commit wins; later commits return a conflict error.
func (d *DB) CommitVerdict(ctx context.Context, v domain.FinalVerdict) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO final_verdicts (run_id, type, credential, event_seq) VALUES (?, ?, ?, ?)`,
		v.RunID, string(v.Type), v.Credential, v.EventSeq)
	return err
}

// GetVerdict loads the committed verdict for a run.
func (d *DB) GetVerdict(ctx context.Context, runID string) (domain.FinalVerdict, error) {
	var v domain.FinalVerdict
	err := d.exec.QueryRowContext(ctx,
		`SELECT run_id, type, credential, event_seq FROM final_verdicts WHERE run_id = ?`, runID).
		Scan(&v.RunID, &v.Type, &v.Credential, &v.EventSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.FinalVerdict{}, domain.NewError(domain.CodeRunNotFound, "verdict not found")
	}
	if err != nil {
		return domain.FinalVerdict{}, err
	}
	return v, nil
}

// GetIdempotency loads an idempotency record by key.
func (d *DB) GetIdempotency(ctx context.Context, key string) (domain.IdempotencyRecord, error) {
	var rec domain.IdempotencyRecord
	err := d.exec.QueryRowContext(ctx,
		`SELECT key, request_digest, status, response, event_seq FROM idempotency WHERE key = ?`, key).
		Scan(&rec.Key, &rec.RequestDigest, &rec.Status, &rec.Response, &rec.EventSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IdempotencyRecord{}, domain.NewError(domain.CodeRunNotFound, "idempotency record not found")
	}
	if err != nil {
		return domain.IdempotencyRecord{}, err
	}
	return rec, nil
}

// PutIdempotency inserts an idempotency record. The primary key on key makes
// concurrent identical requests race to a single winner.
func (d *DB) PutIdempotency(ctx context.Context, rec domain.IdempotencyRecord) error {
	_, err := d.exec.ExecContext(ctx,
		`INSERT INTO idempotency (key, request_digest, status, response, event_seq) VALUES (?, ?, ?, ?, ?)`,
		rec.Key, rec.RequestDigest, rec.Status, rec.Response, rec.EventSeq)
	return err
}
