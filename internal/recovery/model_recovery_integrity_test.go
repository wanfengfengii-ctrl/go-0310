package recovery_test

import (
	"context"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/recovery"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func TestModel_RecoveryEventStreamIntegrity(t *testing.T) {
	tests := []struct {
		name          string
		checkpoint    int64
		eventSeqs     []int64
		staleLease    bool
		wantIntegrity bool
	}{
		{
			name:       "contiguous event stream reaches checkpoint",
			checkpoint: 2,
			eventSeqs:  []int64{1, 2},
		},
		{
			name:          "missing first event is reported",
			checkpoint:    2,
			eventSeqs:     []int64{2},
			wantIntegrity: true,
		},
		{
			name:          "missing internal event is reported",
			checkpoint:    3,
			eventSeqs:     []int64{1, 3},
			wantIntegrity: true,
		},
		{
			name:          "checkpoint ahead remains reported",
			checkpoint:    2,
			eventSeqs:     []int64{1},
			wantIntegrity: true,
		},
		{
			name:          "event log ahead remains reported",
			checkpoint:    1,
			eventSeqs:     []int64{1, 2},
			wantIntegrity: true,
		},
		{
			name:       "stale lease remains expired",
			staleLease: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sqlite.Open("")
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			ctx := context.Background()
			if err := db.Migrate(ctx); err != nil {
				t.Fatalf("migrate database: %v", err)
			}
			if err := db.CreateRun(ctx, domain.TestRun{
				ID: "run-1", PlanID: "plan-1", Generation: 1,
				Stage: domain.StageBaseline, EventSeq: tt.checkpoint,
			}); err != nil {
				t.Fatalf("create run: %v", err)
			}
			for _, seq := range tt.eventSeqs {
				if err := db.AppendEvent(ctx, domain.RunEvent{
					RunID: "run-1", Seq: seq, Type: "stage_progressed",
				}); err != nil {
					t.Fatalf("append event seq %d: %v", seq, err)
				}
			}
			if tt.staleLease {
				if err := db.AcquireLease(ctx, domain.Lease{
					ID: "lease-1", EquipmentID: "chamber-1", Holder: "operator",
					Token: "token-1", ValidUntilMillis: 100,
				}); err != nil {
					t.Fatalf("acquire stale lease: %v", err)
				}
			}

			report, err := recovery.Recover(ctx, db, func() int64 { return 1_000 })
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			if report.RunsRecovered != 1 {
				t.Errorf("RunsRecovered = %d, want 1", report.RunsRecovered)
			}
			if got := report.IntegrityError != ""; got != tt.wantIntegrity {
				t.Errorf("IntegrityError = %q, want present=%t", report.IntegrityError, tt.wantIntegrity)
			}
			if tt.staleLease {
				if report.LeasesExpired != 1 {
					t.Errorf("LeasesExpired = %d, want 1", report.LeasesExpired)
				}
				if _, err := db.GetLease(ctx, "chamber-1"); !domain.Is(err, domain.CodeLeaseConflict) {
					t.Errorf("stale lease remains available after recovery: %v", err)
				}
			}
		})
	}
}
