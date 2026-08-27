package recovery

import (
	"context"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func TestRecoverExpiresStaleLeases(t *testing.T) {
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.CreateRun(ctx, domain.TestRun{ID: "r1", PlanID: "p1", Generation: 1, Stage: domain.StageBaseline}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := db.AcquireLease(ctx, domain.Lease{
		ID: "l1", EquipmentID: "chamber", Holder: "op", Token: "tok", ValidUntilMillis: 100,
	}); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	rep, err := Recover(ctx, db, func() int64 { return 1000 })
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if rep.LeasesExpired != 1 {
		t.Fatalf("leases expired = %d; want 1", rep.LeasesExpired)
	}
	if rep.RunsRecovered != 1 {
		t.Fatalf("runs recovered = %d; want 1", rep.RunsRecovered)
	}
	if _, err := db.GetLease(ctx, "chamber"); !domain.Is(err, domain.CodeLeaseConflict) {
		t.Fatalf("stale lease still present: %v", err)
	}
}

func TestRecoverReportsCheckpointMismatch(t *testing.T) {
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Run claims EventSeq 1 but no event is logged.
	if err := db.CreateRun(ctx, domain.TestRun{ID: "r1", PlanID: "p1", Generation: 1, Stage: domain.StageBaseline, EventSeq: 1}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	rep, err := Recover(ctx, db, func() int64 { return 1000 })
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if rep.IntegrityError == "" {
		t.Fatalf("expected an integrity error to be reported")
	}
}
