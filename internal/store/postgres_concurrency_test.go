package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"aichatdeck/internal/db"
	"aichatdeck/internal/model"
)

// race runs fn from n goroutines released at once and counts successes. The
// pool is filled first: otherwise the goroutine that gets the one idle
// connection finishes before the others have even dialed, and nothing races.
func race(t *testing.T, d *db.DB, n int, fn func() bool) int {
	d.SetMaxIdleConns(n)
	conns := make([]*sql.Conn, n)
	for i := range conns {
		c, err := d.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c
	}
	for _, c := range conns {
		c.Close()
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	wins := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if fn() {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	return wins
}

func TestConcurrentVerificationClaimHandsOutOneLease(t *testing.T) {
	s, task, _ := submittedFixture(t)
	wins := race(t, s.Tasks.db, 16, func() bool {
		job, err := s.Tasks.ClaimVerification(context.Background(), task.CreatedBy)
		if err != nil {
			t.Error(err)
		}
		return job != nil
	})
	if wins != 1 {
		t.Fatalf("expected exactly one verification lease, got %d", wins)
	}
}

func TestConcurrentTaskClaimHasOneWinner(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	creator := mustCreateAgent(t, s.Agents)
	task, err := s.Tasks.Create(ctx, creator.AgentID, model.CreateTaskRequest{Objective: "contended"}, "")
	if err != nil {
		t.Fatal(err)
	}
	workers := make(chan string, 16)
	for i := 0; i < 16; i++ {
		workers <- mustCreateAgent(t, s.Agents).AgentID
	}
	wins := race(t, s.Tasks.db, 16, func() bool {
		_, err := s.Tasks.Claim(ctx, task.TaskID, <-workers)
		return err == nil
	})
	if wins != 1 {
		t.Fatalf("expected one claim winner, got %d", wins)
	}
}

func TestConcurrentFirstLeadershipHasOneWinner(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	owner := mustCreateAgent(t, s.Agents)
	projects := NewProjectStore(s.Tasks.db)
	p, err := projects.Create(ctx, owner.AgentID, model.CreateProjectRequest{Name: "contended"})
	if err != nil {
		t.Fatal(err)
	}
	agents := make(chan string, 8)
	for i := 0; i < 8; i++ {
		agents <- mustCreateAgent(t, s.Agents).AgentID
	}
	wins := race(t, s.Tasks.db, 8, func() bool {
		id := <-agents
		_, err := projects.ChangeLeadership(ctx, p.ProjectID, id, id, 0)
		return err == nil
	})
	if wins != 1 {
		t.Fatalf("expected one leader, got %d", wins)
	}
}
