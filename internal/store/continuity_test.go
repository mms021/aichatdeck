package store

import (
	"aichatdeck/internal/db"
	"aichatdeck/internal/db/dbtest"
	"aichatdeck/internal/model"
	"context"
	"testing"
)

func TestLeadershipExpiryAndDurability(t *testing.T) {
	path := dbtest.URL(t)
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	agents := NewAgentStore(database)
	a, err := agents.Create(ctx, model.RegisterRequest{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := agents.Create(ctx, model.RegisterRequest{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewProjectStore(database)
	p, err := s.Create(ctx, a.AgentID, model.CreateProjectRequest{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ChangeLeadership(ctx, p.ProjectID, a.AgentID, a.AgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeLeadership(ctx, p.ProjectID, b.AgentID, b.AgentID, 0); err == nil {
		t.Fatal("second leader accepted")
	}
	if err = s.SaveProgress(ctx, p.ProjectID, a.AgentID, "", "", first.Generation, map[string]any{"next": "continue"}); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("UPDATE project_leadership SET lease_until='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	second, err := s.ChangeLeadership(ctx, p.ProjectID, b.AgentID, b.AgentID, 0)
	if err != nil || second.Generation <= first.Generation {
		t.Fatalf("takeover: %+v %v", second, err)
	}
	if err = s.SaveProgress(ctx, p.ProjectID, a.AgentID, "", "", first.Generation, map[string]any{"next": "stale"}); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
	database.Close()
	database, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s = NewProjectStore(database)
	got, err := s.Leadership(ctx, p.ProjectID)
	if err != nil || got != second {
		t.Fatalf("lost leadership: %+v %v", got, err)
	}
	checkpoints, err := s.Progress(ctx, p.ProjectID)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("lost progress: %+v %v", checkpoints, err)
	}
}
