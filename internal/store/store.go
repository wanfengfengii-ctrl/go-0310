// Package store defines the transactional persistence boundary shared by the
// run aggregate, plan catalog, equipment leases, measurements, anomalies and
// final verdicts.
//
// A Store exposes both direct data operations and a WithTx escape hatch so the
// workflow engines can commit stage progression, evidence indexing, lease
// changes and idempotency responses atomically. The concrete implementation
// (see internal/store/sqlite) must replay committed events on restart and make
// uncommitted transactions invisible.
package store

import (
	"context"

	"thermal-vacuum-test-gate/internal/domain"
)

// DataOps is the full set of data operations every store (top-level or
// transactional) supports. It is deliberately narrow: consumers depend on this
// interface rather than a concrete database.
type DataOps interface {
	// Plans
	SavePlan(ctx context.Context, p domain.TestPlan) error
	GetPlan(ctx context.Context, id string) (domain.TestPlan, error)

	// Runs and append-only events
	CreateRun(ctx context.Context, r domain.TestRun) error
	GetRun(ctx context.Context, id string) (domain.TestRun, error)
	ListRuns(ctx context.Context) ([]domain.TestRun, error)
	AppendEvent(ctx context.Context, e domain.RunEvent) error
	Events(ctx context.Context, runID string, afterSeq int64) ([]domain.RunEvent, error)
	UpdateRun(ctx context.Context, r domain.TestRun) error

	// Baseline
	SaveBaseline(ctx context.Context, b domain.Baseline) error
	GetBaseline(ctx context.Context, runID string) (domain.Baseline, error)

	// Equipment leases
	AcquireLease(ctx context.Context, l domain.Lease) error
	GetLease(ctx context.Context, equipmentID string) (domain.Lease, error)
	RenewLease(ctx context.Context, equipmentID, token string, validUntilMillis int64) error
	ReleaseLease(ctx context.Context, equipmentID, token string) error
	ExpireLeasesBefore(ctx context.Context, atMillis int64) (int64, error)

	// Measurements and evidence windows
	AppendMeasurement(ctx context.Context, m domain.Measurement) error
	Measurements(ctx context.Context, runID string, stage domain.StageName, cycle, generation int) ([]domain.Measurement, error)
	AllMeasurements(ctx context.Context, runID string) ([]domain.Measurement, error)
	MaxMeasurementTime(ctx context.Context, runID string) (int64, error)
	SaveWindow(ctx context.Context, w domain.EvidenceWindow) error
	GetWindow(ctx context.Context, runID string, stage domain.StageName, cycle int) (domain.EvidenceWindow, error)

	// Scripted acquisition attempts
	AppendCall(ctx context.Context, c domain.MeasurementCall) error
	GetCall(ctx context.Context, id string) (domain.MeasurementCall, error)
	Calls(ctx context.Context, equipmentID string) ([]domain.MeasurementCall, error)

	// Anomalies and retest generations
	SaveAnomaly(ctx context.Context, a domain.Anomaly) error
	GetAnomaly(ctx context.Context, id string) (domain.Anomaly, error)
	SaveRetestGeneration(ctx context.Context, rg domain.RetestGeneration) error
	GetRetestGeneration(ctx context.Context, runID string) (domain.RetestGeneration, error)

	// Reviews, verdicts, idempotency
	AddReview(ctx context.Context, r domain.Review) error
	Reviews(ctx context.Context, runID string) ([]domain.Review, error)
	CommitVerdict(ctx context.Context, v domain.FinalVerdict) error
	GetVerdict(ctx context.Context, runID string) (domain.FinalVerdict, error)
	GetIdempotency(ctx context.Context, key string) (domain.IdempotencyRecord, error)
	PutIdempotency(ctx context.Context, rec domain.IdempotencyRecord) error
}

// Store is a data operation surface plus lifecycle management.
type Store interface {
	DataOps
	Close() error
	Migrate(ctx context.Context) error
	WithTx(ctx context.Context, fn func(Tx) error) error
}

// Tx is a Store whose data operations run inside a single transaction. Commit
// and Rollback finalise the transaction; they are only valid on a Tx returned
// by WithTx.
type Tx interface {
	DataOps
	Commit() error
	Rollback() error
}
