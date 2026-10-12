package httpapi

import (
	"net/http"
	"strings"

	"aichatdeck/internal/model"
)

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	agentID, _ := AgentIDFromContext(r.Context())
	var req model.CreateTaskRequest
	if aerr := decodeJSON(r, &req); aerr != nil {
		s.writeErr(w, aerr)
		return
	}
	task, err := s.App.CreateTask(r.Context(), agentID, req, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := model.TaskFilter{
		Status: q.Get("status"),
		Cursor: q.Get("cursor"),
	}
	if q.Has("project_id") {
		projectID := q.Get("project_id")
		filter.ProjectID = &projectID
	}
	if caps := q.Get("required_capabilities"); caps != "" {
		filter.RequiredCapabilities = strings.Split(caps, ",")
	}
	if tools := q.Get("required_tools"); tools != "" {
		filter.RequiredTools = strings.Split(tools, ",")
	}
	agentID, _ := AgentIDFromContext(r.Context())
	tasks, next, err := s.App.ListTasks(r.Context(), agentID, filter)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "next_cursor": next})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	agentID, _ := AgentIDFromContext(r.Context())
	task, err := s.App.GetTask(r.Context(), agentID, r.PathValue("task_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleClaimTask(w http.ResponseWriter, r *http.Request) {
	agentID, _ := AgentIDFromContext(r.Context())
	task, err := s.App.ClaimTask(r.Context(), agentID, r.PathValue("task_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleHeartbeatTask(w http.ResponseWriter, r *http.Request) {
	agentID, _ := AgentIDFromContext(r.Context())
	var req struct {
		ClaimID string `json:"claim_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	task, err := s.App.HeartbeatTask(r.Context(), agentID, r.PathValue("task_id"), req.ClaimID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleVerifyTask(w http.ResponseWriter, r *http.Request) {
	agentID, _ := AgentIDFromContext(r.Context())
	var req model.VerifyRequest
	if aerr := decodeJSON(r, &req); aerr != nil {
		s.writeErr(w, aerr)
		return
	}
	task, verification, err := s.App.VerifyTask(r.Context(), agentID, r.PathValue("task_id"), req)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.VerifyResponse{Task: task, Verification: verification})
}

func (s *Server) handleClaimVerification(w http.ResponseWriter, r *http.Request) {
	agentID, _ := AgentIDFromContext(r.Context())
	job, err := s.Tasks.ClaimVerification(r.Context(), agentID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}
