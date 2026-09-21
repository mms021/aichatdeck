package model

import (
	"math"
	"time"
)

// AvailabilityRequest is a public runtime declaration, not a provider quota
// measurement or a platform-enforced allocation. Unknown budgets stay unknown.
type AvailabilityRequest struct {
	State            string     `json:"state"`
	Load             float64    `json:"load"`
	FreeSlots        int        `json:"free_slots"`
	MaxParallel      int        `json:"max_parallel"`
	BudgetState      string     `json:"budget_state"`
	NextAvailableAt  *time.Time `json:"next_available_at,omitempty"`
	MaxTaskSeconds   int        `json:"max_task_seconds,omitempty"`
	MaxArtifactBytes int        `json:"max_artifact_bytes,omitempty"`
}

func (r *AvailabilityRequest) Validate() *APIError {
	switch r.State {
	case "available", "busy", "no_budget", "offline":
	default:
		return ValidationError("invalid availability state")
	}
	switch r.BudgetState {
	case "unknown", "available", "low", "exhausted":
	default:
		return ValidationError("invalid budget_state")
	}
	if math.IsNaN(r.Load) || math.IsInf(r.Load, 0) || r.Load < 0 || r.Load > 1 {
		return ValidationError("load must be between 0 and 1")
	}
	if r.MaxParallel < 0 || r.MaxParallel > 10000 || r.FreeSlots < 0 || r.FreeSlots > r.MaxParallel {
		return ValidationError("free_slots must be between 0 and max_parallel (max 10000)")
	}
	if r.MaxTaskSeconds < 0 || r.MaxArtifactBytes < 0 {
		return ValidationError("resource bounds must be non-negative")
	}
	if (r.State == "offline" || r.State == "no_budget" || r.BudgetState == "exhausted") && r.FreeSlots != 0 {
		return ValidationError("unavailable agent must have zero free slots")
	}
	return nil
}

type Availability struct {
	AvailabilityRequest
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Stale     bool      `json:"stale"`
	Source    string    `json:"source"`
}

// AgentProfile intentionally omits arbitrary registration metadata, operator
// identity, evidence links and constraints which may contain private context.
type AgentProfile struct {
	AgentID      string        `json:"agent_id"`
	Capabilities []Capability  `json:"capabilities"`
	Tools        []Tool        `json:"tools"`
	Availability *Availability `json:"availability"` // null means never reported
}
