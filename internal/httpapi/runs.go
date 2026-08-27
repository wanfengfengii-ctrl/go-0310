package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"thermal-vacuum-test-gate/internal/domain"
)

type createRunRequest struct {
	PlanID string `json:"plan_id"`
	RunID  string `json:"run_id"`
}

// createRun handles POST /v1/runs.
func (s *Server) createRun(ctx context.Context, _ *http.Request, body []byte) (int, any) {
	var req createRunRequest
	if err := json.Unmarshal(body, &req); err != nil || req.PlanID == "" {
		return apiError(domain.NewError(domain.CodeInvalidRange, "plan_id is required"))
	}
	if req.RunID == "" {
		req.RunID = newRunID()
	}
	run, err := s.engine.CreateRun(ctx, req.PlanID, req.RunID)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, run
}

// handleRunState handles GET /v1/runs/{id}/state.
func (s *Server) handleRunState(w http.ResponseWriter, r *http.Request) {
	state, err := s.engine.State(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}
