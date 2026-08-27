package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
)

func TestRestartRecoveryPersistsState(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "gate.db")

	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	plan := domain.TestPlan{ID: "p1", SpecimenID: "s", CalibrationSummary: "cal", Cycles: 2}
	if err := db.SavePlan(ctx, plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := db.CreateRun(ctx, domain.TestRun{
		ID: "r1", PlanID: "p1", PlanVersion: 1, Generation: 1,
		Stage: domain.StageColdSoak, CurrentCycle: 1, CompletedCycles: 0, EventSeq: 2,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := db.AppendEvent(ctx, domain.RunEvent{Seq: 1, RunID: "r1", Type: "run.created"}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := db.AcquireLease(ctx, domain.Lease{
		ID: "l1", EquipmentID: "chamber", Holder: "op", Token: "tok", ValidUntilMillis: 9999,
	}); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and verify state survives.
	db2, err := Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gotPlan, err := db2.GetPlan(ctx, "p1")
	if err != nil || gotPlan.Cycles != 2 {
		t.Fatalf("plan not recovered: %v %v", gotPlan, err)
	}
	gotRun, err := db2.GetRun(ctx, "r1")
	if err != nil || gotRun.Stage != domain.StageColdSoak {
		t.Fatalf("run not recovered: %v %v", gotRun, err)
	}
	lease, err := db2.GetLease(ctx, "chamber")
	if err != nil || lease.Token != "tok" {
		t.Fatalf("lease not recovered: %v %v", lease, err)
	}
}

func TestTransactionRollbackLeavesNoPartialState(t *testing.T) {
	db, err := Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	err = db.WithTx(ctx, func(tx store.Tx) error {
		if err := tx.CreateRun(ctx, domain.TestRun{ID: "r1", PlanID: "p1", Generation: 1, Stage: domain.StageBaseline}); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.RunEvent{Seq: 1, RunID: "r1", Type: "run.created"}); err != nil {
			return err
		}
		// Simulate a fault after the event write but before the checkpoint.
		return domain.NewError(domain.CodeInternal, "injected fault")
	})
	if err == nil {
		t.Fatalf("expected error from transaction")
	}
	// Neither the run nor the event may be visible after rollback.
	if _, err := db.GetRun(ctx, "r1"); !domain.Is(err, domain.CodeRunNotFound) {
		t.Fatalf("run visible after rollback: %v", err)
	}
	events, err := db.Events(ctx, "r1", 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("events visible after rollback: %v %v", events, err)
	}
}

func TestIdempotencyRoundTrip(t *testing.T) {
	db, err := Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rec := domain.IdempotencyRecord{Key: "k1", RequestDigest: "d1", Status: 200, Response: []byte("{}")}
	if err := db.PutIdempotency(ctx, rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := db.GetIdempotency(ctx, "k1")
	if err != nil || got.RequestDigest != "d1" {
		t.Fatalf("get: %v %v", got, err)
	}
	// Same key again must conflict.
	if err := db.PutIdempotency(ctx, rec); err == nil {
		t.Fatalf("expected conflict on duplicate key")
	}
}
