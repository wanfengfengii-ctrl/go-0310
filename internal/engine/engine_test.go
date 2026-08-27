package engine

import (
	"context"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/lease"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func testPlan() domain.TestPlan {
	return domain.TestPlan{
		ID:                 "plan-1",
		SpecimenID:         "spec-1",
		CalibrationSummary: "cal-2026",
		Cycles:             1,
		Sensors: []domain.SensorSpec{
			{ID: "s1", Group: "g1", CollectorID: "c1"},
			{ID: "s2", Group: "g1", CollectorID: "c1"},
		},
		Stages: []domain.StageSpec{
			{Name: domain.StageEvacuate, Sequence: 1, VacuumTargetMilliPa: 1000},
			{Name: domain.StageColdRamp, Sequence: 2, ColdTargetMilliKelvin: 100_000},
			{Name: domain.StageColdSoak, Sequence: 3, SoakWindowMillis: 1000, RequiredSamples: 2, MaxRangeMilliKelvin: 5000, MaxDriftPPM: 10000, MaxPressureMilliPa: 1000},
			{Name: domain.StageHotRamp, Sequence: 4, HotTargetMilliKelvin: 400_000},
			{Name: domain.StageHotSoak, Sequence: 5, SoakWindowMillis: 1000, RequiredSamples: 2, MaxRangeMilliKelvin: 5000, MaxDriftPPM: 10000, MaxPressureMilliPa: 1000},
			{Name: domain.StageReturnAmb, Sequence: 6},
			{Name: domain.StageRepressurize, Sequence: 7},
		},
	}
}

func newEngine(t *testing.T) (*Engine, *sqlite.DB, *int64) {
	t.Helper()
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := new(int64)
	*now = 1000
	return New(db, func() int64 { return *now }), db, now
}

func lockAndRun(t *testing.T, e *Engine) domain.TestRun {
	t.Helper()
	if err := e.store.SavePlan(context.Background(), testPlan()); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	run, err := e.CreateRun(context.Background(), "plan-1", "run-1")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_, err = e.SubmitBaseline(context.Background(), "run-1", domain.BaselineRequest{
		InstallCheckOK:         true,
		DoorClosed:             true,
		InitialPressureMilliPa: 101_325_000_000,
		SensorZeros:            map[string]int64{"s1": 0, "s2": 0},
	})
	if err != nil {
		t.Fatalf("submit baseline: %v", err)
	}
	return run
}

func leaseFor(t *testing.T, e *Engine, equipmentID, token string, now *int64) {
	t.Helper()
	if err := e.store.AcquireLease(context.Background(), domain.Lease{
		ID:               "lease-" + equipmentID,
		EquipmentID:      equipmentID,
		Holder:           "op",
		Token:            token,
		ValidUntilMillis: *now + 100_000,
	}); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
}

func submit(t *testing.T, e *Engine, now *int64, req domain.MeasurementRequest) {
	t.Helper()
	*now += 100
	if req.AtMillis == 0 {
		req.AtMillis = *now
	}
	if _, err := e.SubmitMeasurement(context.Background(), "run-1", req); err != nil {
		t.Fatalf("submit measurement: %v", err)
	}
}

func TestBaselineThenEvacuate(t *testing.T) {
	e, _, now := newEngine(t)
	lockAndRun(t, e)

	if _, err := e.StartStage(context.Background(), "run-1", domain.StageEvacuate); err != nil {
		t.Fatalf("start evacuate: %v", err)
	}
	leaseFor(t, e, "c1", "tok", now)
	submit(t, e, now, domain.MeasurementRequest{Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 500, CollectorID: "c1", LeaseToken: "tok"})
	if _, err := e.CompleteStage(context.Background(), "run-1", domain.StageEvacuate); err != nil {
		t.Fatalf("complete evacuate: %v", err)
	}
	run, _ := e.GetRun(context.Background(), "run-1")
	if run.Stage != domain.StageColdRamp {
		t.Fatalf("stage = %s; want cold_ramp", run.Stage)
	}
}

func TestSoakEvidenceWindow(t *testing.T) {
	e, _, now := newEngine(t)
	lockAndRun(t, e)
	leaseFor(t, e, "c1", "tok", now)
	submit(t, e, now, domain.MeasurementRequest{Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 500, CollectorID: "c1", LeaseToken: "tok"})
	if _, err := e.CompleteStage(context.Background(), "run-1", domain.StageEvacuate); err != nil {
		t.Fatalf("complete evacuate: %v", err)
	}
	submit(t, e, now, domain.MeasurementRequest{Stage: domain.StageColdRamp, Cycle: 1, SensorID: "s1", TemperatureMilliKelvin: 90_000, CollectorID: "c1", LeaseToken: "tok"})
	if _, err := e.CompleteStage(context.Background(), "run-1", domain.StageColdRamp); err != nil {
		t.Fatalf("complete cold_ramp: %v", err)
	}
	for _, s := range []string{"s1", "s2"} {
		submit(t, e, now, domain.MeasurementRequest{Stage: domain.StageColdSoak, Cycle: 1, SensorID: s, TemperatureMilliKelvin: 100_000, PressureMilliPa: 100, CollectorID: "c1", LeaseToken: "tok"})
		submit(t, e, now, domain.MeasurementRequest{Stage: domain.StageColdSoak, Cycle: 1, SensorID: s, TemperatureMilliKelvin: 101_000, PressureMilliPa: 100, CollectorID: "c1", LeaseToken: "tok"})
	}
	if _, err := e.CompleteStage(context.Background(), "run-1", domain.StageColdSoak); err != nil {
		t.Fatalf("complete cold_soak: %v", err)
	}
	run, _ := e.GetRun(context.Background(), "run-1")
	if run.Stage != domain.StageHotRamp {
		t.Fatalf("stage = %s; want hot_ramp", run.Stage)
	}
}

func TestSkipStageRejected(t *testing.T) {
	e, _, _ := newEngine(t)
	lockAndRun(t, e)
	if _, err := e.CompleteStage(context.Background(), "run-1", domain.StageHotSoak); !domain.Is(err, domain.CodeStageNotReached) {
		t.Fatalf("expected stage_not_reached, got %v", err)
	}
}

func TestExpiredLeaseRejectsReading(t *testing.T) {
	e, db, now := newEngine(t)
	lockAndRun(t, e)
	leaseFor(t, e, "c1", "tok", now)
	// Expire all leases (simulating a later logical time).
	if _, err := db.ExpireLeasesBefore(context.Background(), 100_000_000); err != nil {
		t.Fatalf("expire: %v", err)
	}
	_, err := e.SubmitMeasurement(context.Background(), "run-1", domain.MeasurementRequest{
		Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 500,
		CollectorID: "c1", LeaseToken: "tok", AtMillis: 5000,
	})
	if !domain.Is(err, domain.CodeLeaseExpired) {
		t.Fatalf("expected lease expired, got %v", err)
	}
	events, _ := db.Events(context.Background(), "run-1", 0)
	found := false
	for _, ev := range events {
		if ev.Type == "measurement.rejected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a rejection event to be archived")
	}
}

func TestGenerationMismatchRejected(t *testing.T) {
	e, _, now := newEngine(t)
	lockAndRun(t, e)
	leaseFor(t, e, "c1", "tok", now)
	_, err := e.SubmitMeasurement(context.Background(), "run-1", domain.MeasurementRequest{
		Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 500,
		CollectorID: "c1", LeaseToken: "tok", Generation: 2, AtMillis: 5000,
	})
	if !domain.Is(err, domain.CodeInvalidGeneration) {
		t.Fatalf("expected invalid generation, got %v", err)
	}
}

func TestTimeRegressionRejected(t *testing.T) {
	e, _, now := newEngine(t)
	lockAndRun(t, e)
	leaseFor(t, e, "c1", "tok", now)
	submit(t, e, now, domain.MeasurementRequest{Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 500, CollectorID: "c1", LeaseToken: "tok", AtMillis: 5000})
	_, err := e.SubmitMeasurement(context.Background(), "run-1", domain.MeasurementRequest{
		Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 400,
		CollectorID: "c1", LeaseToken: "tok", AtMillis: 4000,
	})
	if !domain.Is(err, domain.CodeTimeRegression) {
		t.Fatalf("expected time regression, got %v", err)
	}
}

func TestScriptedAcquisitionAttempts(t *testing.T) {
	e, _, now := newEngine(t)
	lockAndRun(t, e)
	leaseFor(t, e, "c1", "tok", now)

	acq := lease.NewAcquirer()
	acq.Script("c1",
		domain.AcquireOutcome{Success: false, FailureType: domain.FailureTimeout},
		domain.AcquireOutcome{Success: false, FailureType: domain.FailureFormat},
		domain.AcquireOutcome{Success: true, TemperatureMilliKelvin: 100_000, PressureMilliPa: 100},
	)
	e.SetAcquirer(acq)

	for i := 1; i <= 3; i++ {
		*now += 100
		call, m, err := e.CollectMeasurement(context.Background(), "run-1", domain.MeasurementRequest{
			Stage: domain.StageEvacuate, SensorID: "s1", CollectorID: "c1", LeaseToken: "tok",
		})
		if call.Attempt != i {
			t.Fatalf("attempt = %d; want %d", call.Attempt, i)
		}
		if i < 3 {
			if err == nil {
				t.Fatalf("attempt %d should fail", i)
			}
			continue
		}
		if err != nil {
			t.Fatalf("attempt %d should succeed: %v", i, err)
		}
		if m.ID == "" {
			t.Fatalf("attempt %d should produce a valid measurement", i)
		}
	}
}
