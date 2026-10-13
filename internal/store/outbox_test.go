package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"aichatdeck/internal/model"
)

func outboxEvents(t *testing.T, s *AgentStoreSet, taskID string) []string {
	t.Helper()
	rows, err := s.Tasks.db.Query(`SELECT event_type FROM outbox WHERE aggregate_id=? ORDER BY id`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestTaskLifecycleWritesOutboxEvents(t *testing.T) {
	ctx := context.Background()
	s, task, msg := submittedFixture(t)
	other := mustCreateAgent(t, s.Agents)

	// A losing compare-and-swap must not leave an event behind.
	if _, err := s.Tasks.Claim(ctx, task.TaskID, other.AgentID); err == nil {
		t.Fatal("second claim succeeded")
	}
	if _, _, err := s.Tasks.Verify(ctx, task.TaskID, task.CreatedBy, model.VerifyRequest{TargetMessageID: msg.MessageID, Verdict: "verified"}); err != nil {
		t.Fatal(err)
	}
	want := []string{EventTaskCreated, EventTaskClaimed, EventTaskSubmitted, EventTaskVerified}
	if got := outboxEvents(t, s, task.TaskID); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	var status, verdict string
	if err := s.Tasks.db.QueryRow(`SELECT payload->>'status', payload->>'verdict' FROM outbox WHERE aggregate_id=? AND event_type=?`,
		task.TaskID, EventTaskVerified).Scan(&status, &verdict); err != nil {
		t.Fatal(err)
	}
	if status != model.TaskCompleted || verdict != "verified" {
		t.Fatalf("verified payload: status=%s verdict=%s", status, verdict)
	}
}

func TestRequeueAndCancelWriteOutboxEvents(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	creator, worker := mustCreateAgent(t, s.Agents), mustCreateAgent(t, s.Agents)
	expiring, err := s.Tasks.Create(ctx, creator.AgentID, model.CreateTaskRequest{Objective: "expires"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tasks.Claim(ctx, expiring.TaskID, worker.AgentID); err != nil {
		t.Fatal(err)
	}
	if err := s.Tasks.requeueExpiredAt(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, want := outboxEvents(t, s, expiring.TaskID), []string{EventTaskCreated, EventTaskClaimed, EventTaskRequeued}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requeue events = %v, want %v", got, want)
	}

	cancelled, err := s.Tasks.Create(ctx, creator.AgentID, model.CreateTaskRequest{Objective: "cancel"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tasks.CancelOpen(ctx, creator.AgentID, cancelled.TaskID, "not needed"); err != nil {
		t.Fatal(err)
	}
	if got, want := outboxEvents(t, s, cancelled.TaskID), []string{EventTaskCreated, EventTaskCancelled}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel events = %v, want %v", got, want)
	}
}
