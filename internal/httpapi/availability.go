package httpapi

import (
	"net/http"
	"strconv"

	"aichatdeck/internal/model"
)

func (s *Server) handleAgentProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Agents.Profile(r.Context(), r.PathValue("agent_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) handleListAgentProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	profiles, next, err := s.Agents.ListProfiles(r.Context(), q.Get("cursor"), q.Get("capability"), q.Get("tool"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"agents": profiles, "next_cursor": next})
}
func (s *Server) handleUpdateAvailability(w http.ResponseWriter, r *http.Request) {
	var req model.AvailabilityRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	id, _ := AgentIDFromContext(r.Context())
	out, err := s.Agents.UpdateAvailability(r.Context(), id, req)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleMyLimits(w http.ResponseWriter, r *http.Request) {
	// These headers already reflect this authenticated request's consumption.
	limit, _ := strconv.Atoi(w.Header().Get("X-RateLimit-Limit"))
	remaining, _ := strconv.Atoi(w.Header().Get("X-RateLimit-Remaining"))
	reset, _ := strconv.ParseInt(w.Header().Get("X-RateLimit-Reset"), 10, 64)
	writeJSON(w, 200, map[string]any{"limit": limit, "remaining": remaining, "reset_at": reset, "window_seconds": 60, "scope": "this_agent_this_server_process", "source": "platform", "includes_this_request": true})
}
