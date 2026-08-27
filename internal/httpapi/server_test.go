package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := sqlite.Open("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewServer(db, func() int64 { return 1_000_000 })
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d; want 200", rec.Code)
	}
}

func TestStatusForMapping(t *testing.T) {
	cases := []struct {
		code domain.Code
		want int
	}{
		{domain.CodeInvalidRange, http.StatusUnprocessableEntity},
		{domain.CodeIdempotencyConflict, http.StatusConflict},
		{domain.CodeLeaseExpired, http.StatusGone},
		{domain.CodePlanNotFound, http.StatusNotFound},
		{domain.CodeIdempotencyMissing, http.StatusBadRequest},
		{domain.CodeRunFrozen, http.StatusLocked},
		{domain.CodeInternal, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := StatusFor(c.code); got != c.want {
			t.Fatalf("StatusFor(%s) = %d; want %d", c.code, got, c.want)
		}
	}
}

func TestWriteRequiresIdempotencyKey(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/plans/lock", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}
