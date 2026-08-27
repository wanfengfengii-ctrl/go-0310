package anomaly

import (
	"context"
	"reflect"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func TestModel_RejectsAnomalyWhileRetestGenerationIsPending(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate store: %v", err)
	}

	plan := domain.TestPlan{
		ID: "plan-pending-retest", Version: 1, SpecimenID: "specimen-1",
		CalibrationSummary: "valid", Cycles: 1,
		Sensors: []domain.SensorSpec{
			{ID: "s1", Group: "pair", CollectorID: "collector-1"},
			{ID: "s2", Group: "pair", CollectorID: "collector-2"},
			{ID: "s3", Group: "other", CollectorID: "collector-3"},
		},
		Stages: []domain.StageSpec{
			{Name: domain.StageColdSoak, Sequence: 1},
			{Name: domain.StageHotSoak, Sequence: 2, Dependencies: []domain.StageName{domain.StageColdSoak}},
		},
	}
	if err := db.SavePlan(ctx, plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := db.CreateRun(ctx, domain.TestRun{
		ID: "run-pending-retest", PlanID: plan.ID, PlanVersion: plan.Version,
		Generation: 1, Stage: domain.StageColdSoak, CurrentCycle: 1, EventSeq: 7,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	generator := NewGenerator(db, func() int64 { return 123456 })
	var secondConflictCode domain.Code
	cases := []struct {
		name             string
		sensorID         string
		summary          string
		basis            string
		wantSuccess      bool
		wantGeneration   int
		wantFreezeReason string
		wantAffected     []string
		wantCoverage     []string
		wantEventCount   int
		anomalyPersisted bool
		rememberConflict bool
		stableConflict   bool
		wantConflictCode domain.Code
	}{
		{
			name: "first anomaly atomically freezes and creates generation", sensorID: "s1",
			summary: "s1 disconnected", basis: "loss",
			wantSuccess: true, wantGeneration: 2, wantFreezeReason: "s1 disconnected",
			wantAffected: []string{"s1", "s2"}, wantCoverage: []string{"cold_soak", "hot_soak"},
			wantEventCount: 1, anomalyPersisted: true,
		},
		{
			name: "different sensor cannot replace pending generation", sensorID: "s3",
			summary: "s3 out of range", basis: "outlier",
			wantGeneration: 2, wantFreezeReason: "s1 disconnected",
			wantAffected: []string{"s1", "s2"}, wantCoverage: []string{"cold_soak", "hot_soak"},
			wantEventCount: 1, rememberConflict: true,
		},
		{
			name: "repeated rejected fact keeps stable conflict semantics", sensorID: "s3",
			summary: "s3 out of range", basis: "outlier",
			wantGeneration: 2, wantFreezeReason: "s1 disconnected",
			wantAffected: []string{"s1", "s2"}, wantCoverage: []string{"cold_soak", "hot_soak"},
			wantEventCount: 1, stableConflict: true,
		},
		{
			name: "repeated recorded fact remains an idempotent conflict", sensorID: "s1",
			summary: "s1 disconnected", basis: "loss",
			wantGeneration: 2, wantFreezeReason: "s1 disconnected",
			wantAffected: []string{"s1", "s2"}, wantCoverage: []string{"cold_soak", "hot_soak"},
			wantEventCount: 1, anomalyPersisted: true,
			wantConflictCode: domain.CodeGenerationConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg, createErr := generator.CreateAnomaly(ctx, "run-pending-retest", tc.sensorID, tc.summary, tc.basis)
			if tc.wantSuccess {
				if createErr != nil {
					t.Fatalf("create first anomaly: %v", createErr)
				}
				if rg.Generation != tc.wantGeneration || !reflect.DeepEqual(rg.Affected, tc.wantAffected) || !reflect.DeepEqual(rg.Coverage, tc.wantCoverage) {
					t.Fatalf("returned retest = %+v; want generation %d, affected %v, coverage %v", rg, tc.wantGeneration, tc.wantAffected, tc.wantCoverage)
				}
			} else {
				de, ok := createErr.(*domain.Error)
				if !ok || (de.Code != domain.CodeRunFrozen && de.Code != domain.CodeGenerationConflict) {
					t.Fatalf("create anomaly error = %v; want run_frozen or retest_generation_conflict", createErr)
				}
				if tc.wantConflictCode != "" && de.Code != tc.wantConflictCode {
					t.Fatalf("conflict code = %q; want %q", de.Code, tc.wantConflictCode)
				}
				if tc.rememberConflict {
					secondConflictCode = de.Code
				}
				if tc.stableConflict && de.Code != secondConflictCode {
					t.Fatalf("repeated conflict code = %q; first conflict code was %q", de.Code, secondConflictCode)
				}
			}

			run, err := db.GetRun(ctx, "run-pending-retest")
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			if !run.Frozen || run.Generation != tc.wantGeneration || run.FreezeReason != tc.wantFreezeReason || run.EventSeq != 8 {
				t.Fatalf("run state = {frozen:%v generation:%d reason:%q event_seq:%d}; want {frozen:true generation:%d reason:%q event_seq:8}", run.Frozen, run.Generation, run.FreezeReason, run.EventSeq, tc.wantGeneration, tc.wantFreezeReason)
			}

			stored, err := generator.CurrentRetest(ctx, run.ID)
			if err != nil {
				t.Fatalf("get current retest: %v", err)
			}
			if stored.Generation != tc.wantGeneration || !reflect.DeepEqual(stored.Affected, tc.wantAffected) || !reflect.DeepEqual(stored.Coverage, tc.wantCoverage) {
				t.Fatalf("stored retest = %+v; want generation %d, affected %v, coverage %v", stored, tc.wantGeneration, tc.wantAffected, tc.wantCoverage)
			}

			events, err := db.Events(ctx, run.ID, 0)
			if err != nil {
				t.Fatalf("get events: %v", err)
			}
			if len(events) != tc.wantEventCount {
				t.Fatalf("event count = %d; want %d", len(events), tc.wantEventCount)
			}
			if len(events) == 1 && (events[0].Seq != 8 || events[0].Type != "anomaly.created" || string(events[0].Payload) != "s1 disconnected" || events[0].AtMillis != 123456) {
				t.Fatalf("first anomaly event changed: %+v", events[0])
			}

			anomalyID := run.ID + "/" + tc.sensorID + "/" + tc.basis
			_, anomalyErr := db.GetAnomaly(ctx, anomalyID)
			if tc.anomalyPersisted && anomalyErr != nil {
				t.Fatalf("expected anomaly %q to be persisted: %v", anomalyID, anomalyErr)
			}
			if !tc.anomalyPersisted && anomalyErr == nil {
				t.Fatalf("rejected anomaly %q was persisted", anomalyID)
			}
		})
	}
}
