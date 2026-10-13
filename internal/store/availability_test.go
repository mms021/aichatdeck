package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aichatdeck/internal/db"
	"aichatdeck/internal/db/dbtest"
	"aichatdeck/internal/model"
)

func TestAvailabilityFreshnessAndPrivacy(t *testing.T) {
	database, err := db.Open(dbtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := NewAgentStore(database)
	ctx := context.Background()
	a, err := s.Create(ctx, model.RegisterRequest{Operator: "private-operator", Constraints: map[string]any{"secret": "private-context"}, Capabilities: []model.Capability{{Name: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Profile(ctx, a.AgentID)
	if err != nil || p.Availability != nil {
		t.Fatalf("initial: %+v %v", p, err)
	}
	req := model.AvailabilityRequest{State: "available", BudgetState: "unknown", FreeSlots: 1, MaxParallel: 2}
	p, err = s.UpdateAvailability(ctx, a.AgentID, req)
	if err != nil || p.Availability.Stale {
		t.Fatalf("fresh: %+v %v", p, err)
	}
	data, _ := json.Marshal(p)
	if strings.Contains(string(data), "private-") {
		t.Fatal("private profile leaked")
	}
	_, err = database.Exec(`UPDATE agent_availability SET updated_at=? WHERE agent_id=?`, time.Now().Add(-6*time.Minute).UTC().Format(time.RFC3339), a.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Profile(ctx, a.AgentID)
	if err != nil || !p.Availability.Stale {
		t.Fatalf("expired: %+v %v", p, err)
	}
	req.FreeSlots = 3
	if _, err = s.UpdateAvailability(ctx, a.AgentID, req); err == nil {
		t.Fatal("accepted excessive slots")
	}
	req.FreeSlots = 1
	req.State = "offline"
	if _, err = s.UpdateAvailability(ctx, a.AgentID, req); err == nil {
		t.Fatal("accepted offline free slots")
	}
	profiles, _, err := s.ListProfiles(ctx, "", "nonexistent", "", 10)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("filter: %+v %v", profiles, err)
	}
}
