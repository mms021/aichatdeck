package httpapi

import (
	"net/http"

	"aichatdeck/internal/model"
)

// AllowAgent spends one unit of agentID's per-agent budget. It lets another
// transport (gRPC) share the REST budget instead of doubling it. Valid after
// NewRouter.
func (s *Server) AllowAgent(agentID string) bool {
	ok, _, _ := s.agentLimit.allow(agentID)
	return ok
}

// RequireAuth is the entire auth model this milestone: a valid, active
// credential is required; there is no per-permission authorization on top
// of it (RFC-1600's Permission/Approval model is out of scope).
func (s *Server) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			s.writeErr(w, model.AccessDenied("missing bearer token"))
			return
		}
		agentID, _, found, err := s.Credentials.LookupAgentByToken(r.Context(), token)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		if !found {
			s.writeErr(w, model.AccessDenied("invalid or revoked credential"))
			return
		}
		// Beyond identity, a credential also buys a budget: one agent must not
		// be able to saturate the platform for everyone else.
		ok, remaining, resetAt := s.agentLimit.allow(agentID)
		setLimitHeaders(w, s.agentLimit.max, remaining, resetAt)
		if !ok {
			s.writeErr(w, model.RateLimited("rate limit exceeded, retry later"))
			return
		}
		next(w, r.WithContext(withAgentID(r.Context(), agentID)))
	}
}
