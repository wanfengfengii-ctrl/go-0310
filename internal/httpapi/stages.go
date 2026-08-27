package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"thermal-vacuum-test-gate/internal/domain"
)

// submitBaseline handles POST /v1/runs/{id}/baseline.
func (s *Server) submitBaseline(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req domain.BaselineRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid baseline payload"))
	}
	b, err := s.engine.SubmitBaseline(ctx, r.PathValue("id"), req)
	if err != nil {
		return apiError(err)
	}
	return http.StatusOK, b
}

// startStage handles POST /v1/runs/{id}/stages/{stage}/start.
func (s *Server) startStage(ctx context.Context, r *http.Request, _ []byte) (int, any) {
	run, err := s.engine.StartStage(ctx, r.PathValue("id"), domain.StageName(r.PathValue("stage")))
	if err != nil {
		return apiError(err)
	}
	return http.StatusOK, run
}

// completeStage handles POST /v1/runs/{id}/stages/{stage}/complete.
func (s *Server) completeStage(ctx context.Context, r *http.Request, _ []byte) (int, any) {
	run, err := s.engine.CompleteStage(ctx, r.PathValue("id"), domain.StageName(r.PathValue("stage")))
	if err != nil {
		return apiError(err)
	}
	return http.StatusOK, run
}

// submitMeasurement handles POST /v1/runs/{id}/measurements.
func (s *Server) submitMeasurement(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req domain.MeasurementRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid measurement payload"))
	}
	m, err := s.engine.SubmitMeasurement(ctx, r.PathValue("id"), req)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, m
}

// collectMeasurement handles POST /v1/runs/{id}/collect: it drives one scripted
// acquisition attempt against a collector and records the deterministic call.
func (s *Server) collectMeasurement(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req domain.MeasurementRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid collect payload"))
	}
	call, m, err := s.engine.CollectMeasurement(ctx, r.PathValue("id"), req)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, map[string]any{"call": call, "measurement": m}
}
