package httpapi

import "net/http"

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	actor, _ := AgentIDFromContext(r.Context())
	task, err := s.Tasks.Get(r.Context(), actor, r.PathValue("task_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if task.ProjectID != nil {
		if err := s.requireProjectWriter(r, *task.ProjectID, actor); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	task, err = s.Tasks.CancelOpen(r.Context(), actor, task.TaskID, req.Reason)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}
