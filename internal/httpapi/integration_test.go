package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func doReq(t *testing.T, h http.Handler, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPlanLockCreateRunRoundTrip(t *testing.T) {
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	srv := NewServer(db, func() int64 { return 1_000_000 })
	h := srv.Handler()

	plan := domain.TestPlan{
		ID:                 "p1",
		SpecimenID:         "spec-1",
		CalibrationSummary: "cal",
		Cycles:             1,
		Sensors:            []domain.SensorSpec{{ID: "s1", Group: "g1", CollectorID: "c1"}},
		Stages: []domain.StageSpec{
			{Name: domain.StageEvacuate, Sequence: 1},
			{Name: domain.StageColdSoak, Sequence: 2, Dependencies: []domain.StageName{domain.StageEvacuate}},
			{Name: domain.StageHotSoak, Sequence: 3, Dependencies: []domain.StageName{domain.StageColdSoak}},
		},
	}
	rec := doReq(t, h, http.MethodPost, "/v1/plans/lock", "k-plan", plan)
	if rec.Code != http.StatusCreated {
		t.Fatalf("lock plan status = %d; body %s", rec.Code, rec.Body.String())
	}

	// Locking the same plan again with the same key replays the result.
	rec = doReq(t, h, http.MethodPost, "/v1/plans/lock", "k-plan", plan)
	if rec.Code != http.StatusCreated {
		t.Fatalf("replay lock plan status = %d", rec.Code)
	}

	// Same key with different content conflicts.
	plan2 := plan
	plan2.SpecimenID = "spec-2"
	rec = doReq(t, h, http.MethodPost, "/v1/plans/lock", "k-plan", plan2)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d; want 409", rec.Code)
	}

	// GET the locked plan.
	rec = doReq(t, h, http.MethodGet, "/v1/plans/p1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get plan status = %d", rec.Code)
	}

	// Create a run.
	rec = doReq(t, h, http.MethodPost, "/v1/runs", "k-run", map[string]string{"plan_id": "p1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create run status = %d; body %s", rec.Code, rec.Body.String())
	}
	var run domain.TestRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.ID == "" {
		t.Fatalf("expected a generated run id")
	}

	// GET state.
	rec = doReq(t, h, http.MethodGet, "/v1/runs/"+run.ID+"/state", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get state status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), run.PlanID) {
		t.Fatalf("state body missing plan id: %s", rec.Body.String())
	}
}
