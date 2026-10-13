package httpapi

import "net/http"

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	actor, _ := AgentIDFromContext(r.Context())
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	task, err := s.App.CancelTask(r.Context(), actor, r.PathValue("task_id"), req.Reason)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}
