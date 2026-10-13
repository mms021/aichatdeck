package httpapi

import (
	"aichatdeck/internal/model"
	"encoding/json"
	"net/http"
)

func (s *Server) handleLeadership(w http.ResponseWriter, r *http.Request) {
	actor, _ := AgentIDFromContext(r.Context())
	p, full, err := s.projectFor(r, r.PathValue("project_id"), actor)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !full {
		s.writeErr(w, model.AccessDenied("membership required"))
		return
	}
	if r.Method == "GET" {
		l, err := s.Projects.Leadership(r.Context(), p.ProjectID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		progress, err := s.Projects.Progress(r.Context(), p.ProjectID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"leadership": l, "checkpoints": progress})
		return
	}
	if !s.isOrchestrator(r, p, actor) || p.Status != model.ProjectActive {
		s.writeErr(w, model.AccessDenied("active project and appointed orchestrator required"))
		return
	}
	var req struct {
		Generation int64  `json:"generation"`
		Target     string `json:"target_agent_id,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	if req.Generation < 0 || req.Generation == 0 && req.Target != "" {
		s.writeErr(w, model.ValidationError("handoff needs current generation"))
		return
	}
	if req.Target == "" {
		req.Target = actor
	}
	if !s.isOrchestrator(r, p, req.Target) {
		s.writeErr(w, model.AccessDenied("target must be appointed orchestrator"))
		return
	}
	l, err := s.Projects.ChangeLeadership(r.Context(), p.ProjectID, actor, req.Target, req.Generation)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, l)
}

func (s *Server) handleSaveProgress(w http.ResponseWriter, r *http.Request) {
	actor, _ := AgentIDFromContext(r.Context())
	id := r.PathValue("project_id")
	if err := s.requireProjectWriter(r, id, actor); err != nil {
		s.writeErr(w, err)
		return
	}
	var req struct {
		TaskID     string         `json:"task_id,omitempty"`
		ClaimID    string         `json:"claim_id,omitempty"`
		Generation int64          `json:"generation,omitempty"`
		Progress   map[string]any `json:"progress"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	if len(req.Progress) == 0 {
		s.writeErr(w, model.ValidationError("progress required"))
		return
	}
	data, _ := json.Marshal(req.Progress)
	if len(data) > 4096 {
		s.writeErr(w, model.ValidationError("progress must be at most 4096 bytes"))
		return
	}
	if req.TaskID == "" {
		p, err := s.Projects.Get(r.Context(), id)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		if !s.isOrchestrator(r, p, actor) {
			s.writeErr(w, model.AccessDenied("orchestrator required"))
			return
		}
	}
	if err := s.Projects.SaveProgress(r.Context(), id, actor, req.TaskID, req.ClaimID, req.Generation, req.Progress); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]bool{"saved": true})
}
