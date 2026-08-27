package anomaly

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

var errModelRetestWrite = errors.New("injected retest generation write failure")

type modelAtomicStore struct {
	store.Store
	cancelAfterCommit context.CancelFunc
	failRetestWrite   bool
}

func (s *modelAtomicStore) WithTx(ctx context.Context, fn func(store.Tx) error) error {
	err := s.Store.WithTx(ctx, func(tx store.Tx) error {
		return fn(&modelAtomicTx{Tx: tx, failRetestWrite: s.failRetestWrite})
	})
	if err == nil && s.cancelAfterCommit != nil {
		s.cancelAfterCommit()
	}
	return err
}

type modelAtomicTx struct {
	store.Tx
	failRetestWrite bool
}

func (tx *modelAtomicTx) SaveRetestGeneration(ctx context.Context, rg domain.RetestGeneration) error {
	if tx.failRetestWrite {
		return errModelRetestWrite
	}
	return tx.Tx.SaveRetestGeneration(ctx, rg)
}

func TestModel_AnomalyAndRetestGenerationCommitAtomically(t *testing.T) {
	cases := []struct {
		name              string
		cancelAfterCommit bool
		failRetestWrite   bool
		wantCreateError   bool
		wantCommitted     bool
	}{
		{
			name:              "successful return remains complete after request cancellation",
			cancelAfterCommit: true,
			wantCommitted:     true,
		},
		{
			name:            "retest persistence failure rolls back anomaly and frozen run",
			failRetestWrite: true,
			wantCreateError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sqlite.Open("")
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.Migrate(context.Background()); err != nil {
				t.Fatalf("migrate store: %v", err)
			}

			plan := domain.TestPlan{
				ID: "atomic-plan", Version: 3, SpecimenID: "specimen", Cycles: 2,
				CalibrationSummary: "calibrated",
				Sensors: []domain.SensorSpec{
					{ID: "sensor-z", Group: "group-a", CollectorID: "collector-1"},
					{ID: "sensor-a", Group: "group-a", CollectorID: "collector-2"},
					{ID: "sensor-m", Group: "group-b", CollectorID: "collector-1"},
					{ID: "sensor-x", Group: "group-x", CollectorID: "collector-x"},
				},
				Stages: []domain.StageSpec{
					{Name: domain.StageHotSoak, Sequence: 2},
					{Name: domain.StageColdSoak, Sequence: 1},
				},
			}
			if err := db.SavePlan(context.Background(), plan); err != nil {
				t.Fatalf("save plan: %v", err)
			}
			initial := domain.TestRun{
				ID: "atomic-run", PlanID: plan.ID, PlanVersion: plan.Version,
				Generation: 7, Stage: domain.StageColdSoak, CurrentCycle: 2, EventSeq: 9,
			}
			if err := db.CreateRun(context.Background(), initial); err != nil {
				t.Fatalf("create run: %v", err)
			}

			ctx := context.Background()
			var cancel context.CancelFunc
			if tc.cancelAfterCommit {
				ctx, cancel = context.WithCancel(ctx)
				t.Cleanup(cancel)
			}
			wrapped := &modelAtomicStore{
				Store: db, cancelAfterCommit: cancel, failRetestWrite: tc.failRetestWrite,
			}
			generator := NewGenerator(wrapped, func() int64 { return 123456 })
			created, createErr := generator.CreateAnomaly(
				ctx, initial.ID, "sensor-z", "measurement point disconnected", "link-loss-42",
			)
			if (createErr != nil) != tc.wantCreateError {
				t.Fatalf("CreateAnomaly error = %v; want error %v", createErr, tc.wantCreateError)
			}

			run, err := db.GetRun(context.Background(), initial.ID)
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			anomalyID := initial.ID + "/sensor-z/link-loss-42"
			anomalyFact, anomalyErr := db.GetAnomaly(context.Background(), anomalyID)
			retest, retestErr := db.GetRetestGeneration(context.Background(), initial.ID)
			events, err := db.Events(context.Background(), initial.ID, initial.EventSeq)
			if err != nil {
				t.Fatalf("get events: %v", err)
			}

			if !tc.wantCommitted {
				if run.Frozen || run.Generation != initial.Generation || run.EventSeq != initial.EventSeq {
					t.Fatalf("run was partially changed after rollback: %+v", run)
				}
				if !domain.Is(anomalyErr, domain.CodeRunNotFound) {
					t.Fatalf("anomaly survived rollback: value=%+v error=%v", anomalyFact, anomalyErr)
				}
				if !domain.Is(retestErr, domain.CodeRunNotFound) {
					t.Fatalf("retest generation survived rollback: value=%+v error=%v", retest, retestErr)
				}
				if len(events) != 0 {
					t.Fatalf("events survived rollback: %+v", events)
				}
				return
			}

			wantAffected := []string{"sensor-a", "sensor-m", "sensor-z"}
			wantCoverage := []string{"cold_soak", "hot_soak"}
			if anomalyErr != nil {
				t.Fatalf("successful CreateAnomaly left no anomaly fact: %v", anomalyErr)
			}
			if anomalyFact.Summary != "measurement point disconnected" || anomalyFact.Basis != "link-loss-42" {
				t.Fatalf("anomaly fact = %+v", anomalyFact)
			}
			if !run.Frozen || run.FreezeReason != "measurement point disconnected" || run.Generation != 8 || run.EventSeq != 10 {
				t.Fatalf("committed run = %+v", run)
			}
			if retestErr != nil {
				t.Fatalf("successful CreateAnomaly returned before retest was visible: %v", retestErr)
			}
			if retest.Generation != run.Generation || !reflect.DeepEqual(retest.Affected, wantAffected) || !reflect.DeepEqual(retest.Coverage, wantCoverage) {
				t.Fatalf("retest generation = %+v; want generation=%d affected=%v coverage=%v", retest, run.Generation, wantAffected, wantCoverage)
			}
			if !reflect.DeepEqual(created, retest) {
				t.Fatalf("CreateAnomaly result = %+v; persisted retest = %+v", created, retest)
			}
			if len(events) != 1 || events[0].Seq != run.EventSeq || events[0].Type != "anomaly.created" || string(events[0].Payload) != "measurement point disconnected" {
				t.Fatalf("committed events = %+v", events)
			}
		})
	}
}
