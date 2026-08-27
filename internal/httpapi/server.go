// Package httpapi exposes the JSON HTTP contract for the thermal-vacuum test
// gate: stable error codes, request digests, deterministic ordering, health
// checks, and the full set of documented /v1 routes.
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"thermal-vacuum-test-gate/internal/anomaly"
	"thermal-vacuum-test-gate/internal/domain"
	"thermal-vacuum-test-gate/internal/engine"
	"thermal-vacuum-test-gate/internal/lease"
	"thermal-vacuum-test-gate/internal/plan"
	"thermal-vacuum-test-gate/internal/store"
)

// Server wires the workflow components behind the HTTP contract.
type Server struct {
	mux      *http.ServeMux
	store    store.Store
	plans    *plan.Catalog
	engine   *engine.Engine
	leases   *lease.Manager
	retest   *anomaly.Generator
	arbiter  *anomaly.Arbiter
	acquirer *lease.Acquirer
}

// NewServer builds a Server and all of its backing components from a store and
// a logical clock.
func NewServer(s store.Store, now func() int64) *Server {
	acq := lease.NewAcquirer()
	eng := engine.New(s, now)
	eng.SetAcquirer(acq)
	srv := &Server{
		mux:      http.NewServeMux(),
		store:    s,
		plans:    plan.NewCatalog(s).SetClock(now),
		engine:   eng,
		leases:   lease.NewManager(s, now),
		retest:   anomaly.NewGenerator(s, now),
		arbiter:  anomaly.NewArbiter(s, now),
		acquirer: acq,
	}
	srv.register()
	return srv
}

// Engine returns the run engine so callers (and tests) can wire the scripted
// acquisition adapter.
func (s *Server) Engine() *engine.Engine { return s.engine }

// LeaseManager returns the lease manager.
func (s *Server) LeaseManager() *lease.Manager { return s.leases }

// PlanCatalog returns the plan catalog.
func (s *Server) PlanCatalog() *plan.Catalog { return s.plans }

// RetestGenerator returns the retest generator.
func (s *Server) RetestGenerator() *anomaly.Generator { return s.retest }

// VerdictArbiter returns the verdict arbiter.
func (s *Server) VerdictArbiter() *anomaly.Arbiter { return s.arbiter }

// Acquirer returns the scripted acquisition adapter for test/operator wiring.
func (s *Server) Acquirer() *lease.Acquirer { return s.acquirer }

// Handler returns the underlying http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) register() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)

	s.mux.HandleFunc("POST /v1/plans/lock", s.wrap(s.lockPlan))
	s.mux.HandleFunc("GET /v1/plans/{id}", s.handleGetPlan)

	s.mux.HandleFunc("POST /v1/runs", s.wrap(s.createRun))
	s.mux.HandleFunc("GET /v1/runs/{id}/state", s.handleRunState)

	s.mux.HandleFunc("POST /v1/runs/{id}/baseline", s.wrap(s.submitBaseline))
	s.mux.HandleFunc("POST /v1/runs/{id}/stages/{stage}/start", s.wrap(s.startStage))
	s.mux.HandleFunc("POST /v1/runs/{id}/measurements", s.wrap(s.submitMeasurement))
	s.mux.HandleFunc("POST /v1/runs/{id}/collect", s.wrap(s.collectMeasurement))
	s.mux.HandleFunc("POST /v1/runs/{id}/stages/{stage}/complete", s.wrap(s.completeStage))

	s.mux.HandleFunc("POST /v1/equipment/{id}/leases", s.wrap(s.acquireLease))
	s.mux.HandleFunc("POST /v1/equipment/{id}/renew", s.wrap(s.renewLease))
	s.mux.HandleFunc("POST /v1/equipment/{id}/release", s.wrap(s.releaseLease))
	s.mux.HandleFunc("GET /v1/measurement-calls/{id}", s.handleGetCall)

	s.mux.HandleFunc("POST /v1/runs/{id}/anomalies", s.wrap(s.createAnomaly))
	s.mux.HandleFunc("GET /v1/runs/{id}/retests/current", s.handleCurrentRetest)
	s.mux.HandleFunc("POST /v1/runs/{id}/reviews", s.wrap(s.addReview))
	s.mux.HandleFunc("POST /v1/runs/{id}/verdicts", s.wrap(s.commitVerdict))
	s.mux.HandleFunc("GET /v1/runs/{id}/verdict", s.handleGetVerdict)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// endpoint is a write operation that returns an HTTP status and a JSON body.
type endpoint func(ctx context.Context, r *http.Request, body []byte) (int, any)

// wrap enforces the Idempotency-Key requirement and replay/conflict semantics
// for every write route. The canonical digest binds the request path and body.
func (s *Server) wrap(e endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, domain.NewError(domain.CodeIdempotencyMissing, "Idempotency-Key header required"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		digest := canonicalDigest(r.URL.Path, body)
		ctx := r.Context()
		if rec, err := s.store.GetIdempotency(ctx, key); err == nil {
			if rec.RequestDigest != digest {
				writeError(w, domain.NewError(domain.CodeIdempotencyConflict, "idempotency key reused with different content"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rec.Status)
			_, _ = w.Write(rec.Response)
			return
		}
		status, resp := e(ctx, r, body)
		respBytes, _ := json.Marshal(resp)
		if status < http.StatusBadRequest {
			rec := domain.IdempotencyRecord{
				Key:           key,
				RequestDigest: digest,
				Status:        status,
				Response:      respBytes,
			}
			if err := s.store.PutIdempotency(ctx, rec); err != nil {
				// Concurrent identical request already committed; replay its result.
				if existing, e2 := s.store.GetIdempotency(ctx, key); e2 == nil && existing.RequestDigest == digest {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(existing.Status)
					_, _ = w.Write(existing.Response)
					return
				}
				writeError(w, domain.NewError(domain.CodeIdempotencyConflict, "idempotency conflict"))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBytes)
	}
}

// StatusFor maps a stable domain code to its HTTP status.
func StatusFor(code domain.Code) int {
	switch code {
	case domain.CodeDependencyCycle, domain.CodeDuplicateSensor,
		domain.CodeInvalidRange, domain.CodeStaleCalibration,
		domain.CodeMissingCalibration, domain.CodeOverflow,
		domain.CodeDivisionByZero, domain.CodeNonPositiveInterval,
		domain.CodeTimeRegression, domain.CodeInvalidStage,
		domain.CodeBaselineMissing, domain.CodeInsufficientEvidence,
		domain.CodeNotQualified, domain.CodeStageNotReached,
		domain.CodeVerdictNotReady, domain.CodeBaselineInvalid,
		domain.CodeInvalidGeneration:
		return http.StatusUnprocessableEntity
	case domain.CodeIdempotencyConflict, domain.CodeLeaseConflict,
		domain.CodeGenerationConflict, domain.CodeVerdictConflict,
		domain.CodeDuplicateReview, domain.CodeConflict:
		return http.StatusConflict
	case domain.CodeLeaseExpired:
		return http.StatusGone
	case domain.CodeIdempotencyMissing:
		return http.StatusBadRequest
	case domain.CodeRunFrozen:
		return http.StatusLocked
	case domain.CodeRunCompleted:
		return http.StatusConflict
	case domain.CodePlanNotFound, domain.CodeRunNotFound, domain.CodeEquipmentNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// apiError converts an error into a status and stable JSON body.
func apiError(err error) (int, any) {
	if de, ok := err.(*domain.Error); ok {
		body := map[string]any{"code": string(de.Code), "message": de.Message}
		if len(de.Reasons) > 0 {
			body["reasons"] = de.Reasons
		}
		return StatusFor(de.Code), body
	}
	return http.StatusInternalServerError, map[string]any{"code": "internal", "message": err.Error()}
}

// writeError writes a domain error as a stable JSON error body.
func writeError(w http.ResponseWriter, err error) {
	status, body := apiError(err)
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
