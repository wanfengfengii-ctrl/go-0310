package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
)

func TestModel_IdempotencyReplaysBusinessFailureAfterEvidenceBackfill(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	plan := domain.TestPlan{
		ID:                 "failure-replay-plan",
		SpecimenID:         "specimen-1",
		CalibrationSummary: "valid calibration",
		Cycles:             1,
		Sensors: []domain.SensorSpec{
			{ID: "s1", Group: "thermal", CollectorID: "collector-1"},
			{ID: "s2", Group: "thermal", CollectorID: "collector-1"},
		},
		Stages: []domain.StageSpec{
			{Name: domain.StageEvacuate, Sequence: 1, VacuumTargetMilliPa: 1_000},
			{Name: domain.StageColdRamp, Sequence: 2, ColdTargetMilliKelvin: 100_000, HotTargetMilliKelvin: 200_000},
			{
				Name: domain.StageColdSoak, Sequence: 3, SoakWindowMillis: 1_000,
				RequiredSamples: 1, MaxRangeMilliKelvin: 1_000,
				MaxDriftPPM: 1_000, MaxPressureMilliPa: 1_000,
			},
		},
	}
	if _, err := srv.PlanCatalog().LockPlan(ctx, plan); err != nil {
		t.Fatalf("lock plan: %v", err)
	}
	if _, err := srv.Engine().CreateRun(ctx, plan.ID, "failure-replay-run"); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := srv.Engine().SubmitBaseline(ctx, "failure-replay-run", domain.BaselineRequest{
		InstallCheckOK: true, DoorClosed: true, InitialPressureMilliPa: 101_325_000_000,
		SensorZeros: map[string]int64{"s1": 0, "s2": 0},
	}); err != nil {
		t.Fatalf("submit baseline: %v", err)
	}
	lease, err := srv.LeaseManager().Acquire(ctx, "collector-1", "operator", 100_000)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	submit := func(req domain.MeasurementRequest) {
		t.Helper()
		req.CollectorID = "collector-1"
		req.LeaseToken = lease.Token
		if _, err := srv.Engine().SubmitMeasurement(ctx, "failure-replay-run", req); err != nil {
			t.Fatalf("submit measurement for %s/%s: %v", req.Stage, req.SensorID, err)
		}
	}
	submit(domain.MeasurementRequest{Stage: domain.StageEvacuate, SensorID: "s1", PressureMilliPa: 500, AtMillis: 1_000_001})
	if _, err := srv.Engine().CompleteStage(ctx, "failure-replay-run", domain.StageEvacuate); err != nil {
		t.Fatalf("complete evacuate: %v", err)
	}
	submit(domain.MeasurementRequest{Stage: domain.StageColdRamp, Cycle: 1, SensorID: "s1", TemperatureMilliKelvin: 90_000, AtMillis: 1_000_002})
	if _, err := srv.Engine().CompleteStage(ctx, "failure-replay-run", domain.StageColdRamp); err != nil {
		t.Fatalf("complete cold ramp: %v", err)
	}
	submit(domain.MeasurementRequest{Stage: domain.StageColdSoak, Cycle: 1, SensorID: "s1", TemperatureMilliKelvin: 100_000, PressureMilliPa: 100, AtMillis: 1_000_003})

	path := "/v1/runs/failure-replay-run/stages/cold_soak/complete"
	first := doReq(t, srv.Handler(), http.MethodPost, path, "complete-key", nil)
	if first.Code != http.StatusUnprocessableEntity {
		t.Fatalf("first completion status = %d, want 422; body=%s", first.Code, first.Body.String())
	}
	var failure map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode first failure: %v", err)
	}
	if failure["code"] != string(domain.CodeInsufficientEvidence) {
		t.Fatalf("first failure code = %v, want %s", failure["code"], domain.CodeInsufficientEvidence)
	}
	firstBody := append([]byte(nil), first.Body.Bytes()...)

	submit(domain.MeasurementRequest{Stage: domain.StageColdSoak, Cycle: 1, SensorID: "s2", TemperatureMilliKelvin: 100_000, PressureMilliPa: 100, AtMillis: 1_000_004})

	cases := []struct {
		name      string
		key       string
		wantCode  int
		wantStage domain.StageName
		replay    bool
	}{
		{name: "old key replays failure without advancing", key: "complete-key", wantCode: http.StatusUnprocessableEntity, wantStage: domain.StageColdSoak, replay: true},
		{name: "new key completes after evidence is sufficient", key: "complete-key-after-backfill", wantCode: http.StatusOK, wantStage: domain.StageHotRamp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doReq(t, srv.Handler(), http.MethodPost, path, tc.key, nil)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.replay && !bytes.Equal(rec.Body.Bytes(), firstBody) {
				t.Fatalf("replayed body = %q, want exact first body %q", rec.Body.Bytes(), firstBody)
			}
			run, err := srv.Engine().GetRun(ctx, "failure-replay-run")
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			if run.Stage != tc.wantStage {
				t.Fatalf("stage = %s, want %s", run.Stage, tc.wantStage)
			}
		})
	}
}
