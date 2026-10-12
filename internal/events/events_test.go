package events

import (
	"context"
	"errors"
	"testing"

	"aichatdeck/internal/db"
	"aichatdeck/internal/db/dbtest"
	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
)

type fakePublisher struct {
	sent []model.TaskEvent
	err  error
}

func (f *fakePublisher) Publish(_ context.Context, batch []model.TaskEvent) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, batch...)
	return nil
}

// fixture creates and claims one task: two outbox events.
func fixture(t *testing.T) (*db.DB, string) {
	t.Helper()
	d, err := db.Open(dbtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	agents, tasks := store.NewAgentStore(d), store.NewTaskStore(d)
	creator, _ := agents.Create(ctx, model.RegisterRequest{})
	worker, _ := agents.Create(ctx, model.RegisterRequest{})
	task, err := tasks.Create(ctx, creator.AgentID, model.CreateTaskRequest{Objective: "relay"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Claim(ctx, task.TaskID, worker.AgentID); err != nil {
		t.Fatal(err)
	}
	return d, task.TaskID
}

func pending(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFlushPublishesInOrderOnce(t *testing.T) {
	d, taskID := fixture(t)
	pub := &fakePublisher{}
	r := &Relay{DB: d, Publisher: pub}
	n, err := r.Flush(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("flush: n=%d err=%v", n, err)
	}
	if pub.sent[0].Type != store.EventTaskCreated || pub.sent[1].Type != store.EventTaskClaimed || pub.sent[0].TaskID != taskID {
		t.Fatalf("unexpected batch: %+v", pub.sent)
	}
	if pub.sent[0].EventID == "" || pub.sent[0].EventID == pub.sent[1].EventID || pub.sent[0].OccurredAt.IsZero() {
		t.Fatalf("events need distinct ids and a timestamp: %+v", pub.sent)
	}
	if pub.sent[1].Seq != pub.sent[0].Seq+1 {
		t.Fatalf("seq must be contiguous in outbox order: %d, %d", pub.sent[0].Seq, pub.sent[1].Seq)
	}
	var stored int64
	if err := d.QueryRow(`SELECT seq FROM outbox WHERE event_id=?`, pub.sent[1].EventID).Scan(&stored); err != nil || stored != pub.sent[1].Seq {
		t.Fatalf("stored seq %d, published %d: %v", stored, pub.sent[1].Seq, err)
	}
	if n, err := r.Flush(context.Background()); n != 0 || err != nil || pending(t, d) != 0 {
		t.Fatalf("second flush resent: n=%d err=%v", n, err)
	}
}

func TestFlushKeepsEventsWhenKafkaFails(t *testing.T) {
	d, _ := fixture(t)
	r := &Relay{DB: d, Publisher: &fakePublisher{err: errors.New("broker down")}}
	if _, err := r.Flush(context.Background()); err == nil {
		t.Fatal("expected publish error")
	}
	if got := pending(t, d); got != 2 {
		t.Fatalf("pending after failure = %d, want 2", got)
	}
}

func TestFlushYieldsToRelayHoldingLock(t *testing.T) {
	d, _ := fixture(t)
	ctx := context.Background()
	holder, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, relayLock); err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}
	if n, err := (&Relay{DB: d, Publisher: pub}).Flush(ctx); n != 0 || err != nil || len(pub.sent) != 0 {
		t.Fatalf("second relay published while lock held: n=%d err=%v", n, err)
	}
}
