package httpapi

import (
	"aichatdeck/internal/model"
	"net/http"
)

func (s *Server) isOrchestrator(r *http.Request, p model.Project, id string) bool {
	if p.Owner == id {
		return true
	}
	m, err := s.Members.Get(r.Context(), p.ProjectID, id)
	return err == nil && m.Status == model.MemberActive && m.Role == model.RoleOrchestrator
}

// Connection is durable and idempotent. Membership approval is independent
// from assignment; joining never grants access to a closed project.
func (s *Server) handleProjectConnection(w http.ResponseWriter, r *http.Request) {
	id, _ := AgentIDFromContext(r.Context())
	p, full, err := s.projectFor(r, r.PathValue("project_id"), id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if p.Status != model.ProjectActive {
		s.writeErr(w, model.Conflict("project is archived"))
		return
	}
	if !full {
		writeJSON(w, 200, map[string]any{"connection": "requires_membership", "project_id": p.ProjectID})
		return
	}
	if r.Method == "POST" && p.Visibility == model.ProjectOpen {
		if _, err := s.Members.Enroll(r.Context(), p.ProjectID, id); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	m, err := s.Members.Get(r.Context(), p.ProjectID, id)
	if err != nil && !model.IsCode(err, model.ErrNotFound) {
		s.writeErr(w, err)
		return
	}
	status := "awaiting_assignment"
	if p.Owner == id || (m.Status == model.MemberActive && (m.Role == model.RoleOrchestrator || m.WorkRole != "" && m.WorkRole != model.WorkRoleUnassigned)) {
		status = "ready"
	}
	if err == nil && m.Status != model.MemberActive {
		status = "requires_membership"
	}
	writeJSON(w, 200, map[string]any{"project_id": p.ProjectID, "agent_id": id, "connection": status, "membership": m,
		"instructions": "Work only on explicitly authorized tasks in this project. Work roles require matching allowed_work_roles. Verification remains creator-only. Poll connection for assignment; do not self-assign."})
}

func (s *Server) handleAssignmentQueue(w http.ResponseWriter, r *http.Request) {
	id, _ := AgentIDFromContext(r.Context())
	p, _, err := s.projectFor(r, r.PathValue("project_id"), id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !s.isOrchestrator(r, p, id) {
		s.writeErr(w, model.AccessDenied("only owner or orchestrator may read assignment queue"))
		return
	}
	members, err := s.Members.List(r.Context(), p.ProjectID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := []any{}
	for _, m := range members {
		if m.AgentID == p.Owner || m.Role == model.RoleOrchestrator || m.Status != model.MemberActive || m.WorkRole != model.WorkRoleUnassigned {
			continue
		}
		profile, err := s.Agents.Profile(r.Context(), m.AgentID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		out = append(out, map[string]any{"membership": m, "profile": profile})
	}
	writeJSON(w, 200, map[string]any{"participants": out, "project_id": p.ProjectID})
}
