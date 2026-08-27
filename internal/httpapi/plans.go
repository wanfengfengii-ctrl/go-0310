package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"thermal-vacuum-test-gate/internal/domain"
)

// lockPlan handles POST /v1/plans/lock.
func (s *Server) lockPlan(ctx context.Context, _ *http.Request, body []byte) (int, any) {
	var p domain.TestPlan
	if err := json.Unmarshal(body, &p); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid plan payload"))
	}
	locked, err := s.plans.LockPlan(ctx, p)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, locked
}

// handleGetPlan handles GET /v1/plans/{id}.
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.plans.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
