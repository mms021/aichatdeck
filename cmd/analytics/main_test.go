package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHandleLeadTimeAndDedup(t *testing.T) {
	a := newAnalytics(prometheus.NewRegistry())
	verified := []byte(`{"event_id":"e1","type":"task.verified","task_id":"t","occurred_at":"2026-01-01T00:01:30Z",
		"data":{"verdict":"verified","created_at":"2026-01-01T00:00:00Z"}}`)
	now := time.Date(2026, 1, 1, 0, 1, 31, 0, time.UTC)
	for i := 0; i < 2; i++ { // second delivery is a redelivery
		if err := a.handle(verified, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(a.events.WithLabelValues("task.verified")); got != 1 {
		t.Fatalf("events = %v, want 1", got)
	}
	if got := testutil.ToFloat64(a.duplicates); got != 1 {
		t.Fatalf("duplicates = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(a.leadTime); n != 1 {
		t.Fatalf("lead time series = %d, want 1", n)
	}
	inconclusive := []byte(`{"event_id":"e2","type":"task.verified","task_id":"t","occurred_at":"2026-01-01T00:02:00Z",
		"data":{"verdict":"inconclusive","created_at":"2026-01-01T00:00:00Z"}}`)
	if err := a.handle(inconclusive, now); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(a.leadTime); n != 1 {
		t.Fatal("inconclusive verdict must not record a lead time")
	}
	if err := a.handle([]byte("not json"), now); err == nil {
		t.Fatal("malformed record accepted")
	}
}

func TestRecentEvictsOldest(t *testing.T) {
	r := newRecent(2)
	for _, id := range []string{"a", "b", "c"} {
		if !r.add(id) {
			t.Fatalf("%s reported as seen", id)
		}
	}
	if !r.add("a") || r.add("c") {
		t.Fatal("expected a evicted and c remembered")
	}
}
