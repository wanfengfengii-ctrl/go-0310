package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"thermal-vacuum-test-gate/internal/domain"
)

type leaseRequest struct {
	Holder    string `json:"holder"`
	TTLMillis int64  `json:"ttl_millis"`
}

type renewRequest struct {
	Token     string `json:"token"`
	TTLMillis int64  `json:"ttl_millis"`
}

type releaseRequest struct {
	Token string `json:"token"`
}

// acquireLease handles POST /v1/equipment/{id}/leases.
func (s *Server) acquireLease(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req leaseRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Holder == "" {
		return apiError(domain.NewError(domain.CodeInvalidRange, "holder is required"))
	}
	lease, err := s.leases.Acquire(ctx, r.PathValue("id"), req.Holder, req.TTLMillis)
	if err != nil {
		return apiError(err)
	}
	return http.StatusCreated, lease
}

// renewLease handles POST /v1/equipment/{id}/renew.
func (s *Server) renewLease(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req renewRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid renew payload"))
	}
	lease, err := s.leases.Renew(ctx, r.PathValue("id"), req.Token, req.TTLMillis)
	if err != nil {
		return apiError(err)
	}
	return http.StatusOK, lease
}

// releaseLease handles POST /v1/equipment/{id}/release.
func (s *Server) releaseLease(ctx context.Context, r *http.Request, body []byte) (int, any) {
	var req releaseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apiError(domain.NewError(domain.CodeInvalidRange, "invalid release payload"))
	}
	if err := s.leases.Release(ctx, r.PathValue("id"), req.Token); err != nil {
		return apiError(err)
	}
	return http.StatusOK, map[string]string{"status": "released"}
}

// handleGetCall handles GET /v1/measurement-calls/{id}.
func (s *Server) handleGetCall(w http.ResponseWriter, r *http.Request) {
	call, err := s.store.GetCall(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, call)
}
