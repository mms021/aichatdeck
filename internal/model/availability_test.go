package model

import (
	"math"
	"testing"
)

func TestAvailabilityValidation(t *testing.T) {
	valid := AvailabilityRequest{State: "available", Load: 0.5, FreeSlots: 1, MaxParallel: 2, BudgetState: "unknown"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AvailabilityRequest){
		"state":          func(r *AvailabilityRequest) { r.State = "invented" },
		"budget":         func(r *AvailabilityRequest) { r.BudgetState = "invented" },
		"negative load":  func(r *AvailabilityRequest) { r.Load = -1 },
		"high load":      func(r *AvailabilityRequest) { r.Load = 2 },
		"nan":            func(r *AvailabilityRequest) { r.Load = math.NaN() },
		"infinite":       func(r *AvailabilityRequest) { r.Load = math.Inf(1) },
		"slots":          func(r *AvailabilityRequest) { r.FreeSlots = 3 },
		"negative slots": func(r *AvailabilityRequest) { r.FreeSlots = -1 },
		"parallel":       func(r *AvailabilityRequest) { r.MaxParallel = 10001 },
		"offline":        func(r *AvailabilityRequest) { r.State = "offline" },
		"no budget":      func(r *AvailabilityRequest) { r.State = "no_budget" },
		"exhausted":      func(r *AvailabilityRequest) { r.BudgetState = "exhausted" },
		"artifact bound": func(r *AvailabilityRequest) { r.MaxArtifactBytes = -1 },
		"task bound":     func(r *AvailabilityRequest) { r.MaxTaskSeconds = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			req := valid
			mutate(&req)
			if req.Validate() == nil {
				t.Fatal("accepted invalid availability")
			}
		})
	}
	valid.State = "offline"
	valid.FreeSlots = 0
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
}
