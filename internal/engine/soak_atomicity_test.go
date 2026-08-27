package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

type modelFaultStore struct {
	store.Store
	failAt string
	active bool
	err    error
}

func (s *modelFaultStore) WithTx(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.WithTx(ctx, func(tx store.Tx) error {
		return fn(&modelFaultTx{Tx: tx, failAt: s.failAt, active: s.active, err: s.err})
	})
}

type modelFaultTx struct {
	store.Tx
	failAt string
	active bool
	err    error
}

func (tx *modelFaultTx) SaveWindow(ctx context.Context, window domain.EvidenceWindow) error {
	if tx.active && tx.failAt == "evidence window" {
		return tx.err
	}
	return tx.Tx.SaveWindow(ctx, window)
}

func (tx *modelFaultTx) UpdateRun(ctx context.Context, run domain.TestRun) error {
	if tx.active && tx.failAt == "run frontier" {
		return tx.err
	}
	return tx.Tx.UpdateRun(ctx, run)
}

func (tx *modelFaultTx) AppendEvent(ctx context.Context, event domain.RunEvent) error {
	if tx.active && tx.failAt == "completion event" {
		return tx.err
	}
	return tx.Tx.AppendEvent(ctx, event)
}

func TestModel_SoakCompletionPersistsEvidenceAndProgressAtomically(t *testing.T) {
	cases := []struct {
		name      string
		failAt    string
		committed bool
	}{
		{name: "successful soak survives restart", committed: true},
		{name: "evidence write failure rolls back", failAt: "evidence window"},
		{name: "frontier write failure rolls back evidence", failAt: "run frontier"},
		{name: "event write failure rolls back evidence and frontier", failAt: "completion event"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dsn := "file:" + filepath.Join(t.TempDir(), "gate.db")
			db, err := sqlite.Open(dsn)
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			if err := db.Migrate(ctx); err != nil {
				_ = db.Close()
				t.Fatalf("migrate database: %v", err)
			}

			injected := errors.New("injected completion write failure")
			faults := &modelFaultStore{Store: db, failAt: tc.failAt, err: injected}
			now := int64(1_000)
			eng := New(faults, func() int64 { return now })
			plan := domain.TestPlan{
				ID: "plan", Version: 1, SpecimenID: "specimen", Cycles: 1,
				CalibrationSummary: "calibrated",
				Sensors: []domain.SensorSpec{
					{ID: "s1", CollectorID: "collector"},
					{ID: "s2", CollectorID: "collector"},
				},
				Stages: []domain.StageSpec{
					{Name: domain.StageEvacuate, Sequence: 1, VacuumTargetMilliPa: 1_000},
					{Name: domain.StageColdRamp, Sequence: 2, ColdTargetMilliKelvin: 100_000},
					{Name: domain.StageColdSoak, Sequence: 3, SoakWindowMillis: 1_000, RequiredSamples: 2, MaxRangeMilliKelvin: 5_000, MaxDriftPPM: 10_000, MaxPressureMilliPa: 1_000},
				},
			}
			if err := faults.SavePlan(ctx, plan); err != nil {
				t.Fatalf("save plan: %v", err)
			}
			if _, err := eng.CreateRun(ctx, plan.ID, "run"); err != nil {
				t.Fatalf("create run: %v", err)
			}
			if _, err := eng.SubmitBaseline(ctx, "run", domain.BaselineRequest{
				InstallCheckOK: true, DoorClosed: true,
				InitialPressureMilliPa: 101_325_000_000,
				SensorZeros:            map[string]int64{"s1": 0, "s2": 0},
			}); err != nil {
				t.Fatalf("submit baseline: %v", err)
			}
			if err := faults.AcquireLease(ctx, domain.Lease{
				ID: "lease", EquipmentID: "collector", Holder: "operator",
				Token: "token", ValidUntilMillis: 100_000,
			}); err != nil {
				t.Fatalf("acquire lease: %v", err)
			}

			submit := func(at int64, stage domain.StageName, cycle int, sensor string, temp, pressure int64) {
				t.Helper()
				_, submitErr := eng.SubmitMeasurement(ctx, "run", domain.MeasurementRequest{
					Stage: stage, Cycle: cycle, SensorID: sensor,
					TemperatureMilliKelvin: temp, PressureMilliPa: pressure,
					CollectorID: "collector", LeaseToken: "token", AtMillis: at,
				})
				if submitErr != nil {
					t.Fatalf("submit %s measurement at %d: %v", stage, at, submitErr)
				}
			}
			submit(1_100, domain.StageEvacuate, 0, "s1", 0, 500)
			if _, err := eng.CompleteStage(ctx, "run", domain.StageEvacuate); err != nil {
				t.Fatalf("complete evacuate: %v", err)
			}
			submit(1_200, domain.StageColdRamp, 1, "s1", 90_000, 500)
			if _, err := eng.CompleteStage(ctx, "run", domain.StageColdRamp); err != nil {
				t.Fatalf("complete cold ramp: %v", err)
			}
			submit(1_300, domain.StageColdSoak, 1, "s1", 100_000, 100)
			submit(1_400, domain.StageColdSoak, 1, "s1", 101_000, 100)
			submit(1_500, domain.StageColdSoak, 1, "s2", 100_000, 100)
			submit(1_600, domain.StageColdSoak, 1, "s2", 101_000, 100)

			before, err := faults.GetRun(ctx, "run")
			if err != nil {
				t.Fatalf("load pre-completion run: %v", err)
			}
			beforeEvents, err := faults.Events(ctx, "run", 0)
			if err != nil {
				t.Fatalf("load pre-completion events: %v", err)
			}
			faults.active = true
			_, completeErr := eng.CompleteStage(ctx, "run", domain.StageColdSoak)
			if tc.committed && completeErr != nil {
				t.Fatalf("complete cold soak: %v", completeErr)
			}
			if !tc.committed && !errors.Is(completeErr, injected) {
				t.Fatalf("completion error = %v; want injected failure", completeErr)
			}

			if err := db.Close(); err != nil {
				t.Fatalf("close before restart: %v", err)
			}
			reopened, err := sqlite.Open(dsn)
			if err != nil {
				t.Fatalf("reopen database: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			if err := reopened.Migrate(ctx); err != nil {
				t.Fatalf("migrate reopened database: %v", err)
			}
			recovered, err := New(reopened, func() int64 { return now }).State(ctx, "run")
			if err != nil {
				t.Fatalf("recover state: %v", err)
			}
			afterEvents, err := reopened.Events(ctx, "run", 0)
			if err != nil {
				t.Fatalf("load recovered events: %v", err)
			}

			if tc.committed {
				if recovered.Run.Stage != domain.StageHotRamp || recovered.Run.EventSeq != before.EventSeq+1 {
					t.Fatalf("recovered frontier = (%s, seq %d); want (hot_ramp, seq %d)", recovered.Run.Stage, recovered.Run.EventSeq, before.EventSeq+1)
				}
				if len(recovered.Windows) != 1 {
					t.Fatalf("recovered windows = %#v; want one passed cold-soak window", recovered.Windows)
				}
				window := recovered.Windows[0]
				if window.RunID != "run" || window.Stage != domain.StageColdSoak || window.Cycle != 1 ||
					window.CoveragePPM != 1_000_000 || window.Samples != 4 ||
					window.RangeMilliKelvin != 1_000 || window.DriftPPM != 1 || !window.Passed {
					t.Fatalf("recovered evidence window = %#v; want passed fixed-point evidence", window)
				}
				if len(afterEvents) != len(beforeEvents)+1 {
					t.Fatalf("event count after completion = %d; want %d", len(afterEvents), len(beforeEvents)+1)
				}
				last := afterEvents[len(afterEvents)-1]
				if last.Type != "stage.completed" || string(last.Payload) != string(domain.StageColdSoak) || last.Seq != recovered.Run.EventSeq {
					t.Fatalf("completion event = %#v; want cold-soak event at frontier sequence", last)
				}
				return
			}

			if recovered.Run.Stage != before.Stage || recovered.Run.CurrentCycle != before.CurrentCycle || recovered.Run.EventSeq != before.EventSeq {
				t.Fatalf("frontier changed after rollback: before=%#v after=%#v", before, recovered.Run)
			}
			if len(recovered.Windows) != 0 {
				t.Fatalf("evidence survived failed completion: %#v", recovered.Windows)
			}
			if len(afterEvents) != len(beforeEvents) {
				t.Fatalf("events survived failed completion: before=%d after=%d", len(beforeEvents), len(afterEvents))
			}
		})
	}
}
