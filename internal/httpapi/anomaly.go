package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"thermal-vacuum-test-gate/internal/domain"
)

type anomalyRequest struct {
	SensorID string `json:"sensor_id"`
	Summary  string `json:"summary"`
	Basis    string `json:"basis"`
}

type reviewRequest struct {
	Reviewer  string `json:"reviewer"`
	Qualified bool   `json:"qualified"`
	Digest    string `json:"digest"`
}

type verdictRequest struct {
	Type domain.VerdictType `json:"type"`
}

// createAnomaly handles POST /v1/runs/{id}/anomalies.
func (s *Server) createAnomaly(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req anomalyRequest
	if err := json.Unmarshal(body, &req); err != nil || req.SensorID == "" {
		return apiError(domain.NewError(domain.CodeInvalidRange, "sensor_id is required"))
	}
	rg, err := s.retest.CreateAnomaly(ctx, r.PathValue("id"), req.SensorID, req.Summary, req.Basis)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, rg
}

// handleCurrentRetest handles GET /v1/runs/{id}/retests/current.
func (s *Server) handleCurrentRetest(w http.ResponseWriter, r *http.Request) {
	rg, err := s.retest.CurrentRetest(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rg)
}

// addReview handles POST /v1/runs/{id}/reviews.
func (s *Server) addReview(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req reviewRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid review payload"))
	}
	rev, err := s.arbiter.AddReview(ctx, r.PathValue("id"), req.Reviewer, req.Qualified, req.Digest)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, rev
}

// commitVerdict handles POST /v1/runs/{id}/verdicts.
func (s *Server) commitVerdict(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req verdictRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid verdict payload"))
	}
	v, err := s.arbiter.CommitVerdict(ctx, r.PathValue("id"), req.Type)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, v
}

// handleGetVerdict handles GET /v1/runs/{id}/verdict.
func (s *Server) handleGetVerdict(w http.ResponseWriter, r *http.Request) {
	v, err := s.arbiter.Verdict(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
