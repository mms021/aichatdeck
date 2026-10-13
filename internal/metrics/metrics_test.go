package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"aichatdeck/internal/db"
	"aichatdeck/internal/db/dbtest"
	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
)

func TestMiddlewareLabelsByRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks/{task_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h := Middleware(mux)
	for _, id := range []string{"a", "b", "c"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/tasks/"+id, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	if got := testutil.ToFloat64(httpRequests.WithLabelValues("GET", "GET /tasks/{task_id}", "404")); got != 3 {
		t.Fatalf("pattern-labelled count = %v, want 3 (IDs must not become labels)", got)
	}
	if got := testutil.ToFloat64(httpRequests.WithLabelValues("GET", "unmatched", "404")); got != 1 {
		t.Fatalf("unmatched count = %v, want 1", got)
	}
}

func TestStateCollectorReadsTaskStatus(t *testing.T) {
	d, err := db.Open(dbtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	agents, tasks := store.NewAgentStore(d), store.NewTaskStore(d)
	creator, err := agents.Create(ctx, model.RegisterRequest{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := agents.Create(ctx, model.RegisterRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		task, err := tasks.Create(ctx, creator.AgentID, model.CreateTaskRequest{Objective: "x"}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.TaskID)
	}
	if _, err := tasks.Claim(ctx, ids[0], worker.AgentID); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(&stateCollector{db: d.DB})
	want := `
# HELP aichatdeck_tasks Tasks by current status.
# TYPE aichatdeck_tasks gauge
aichatdeck_tasks{status="CLAIMED"} 1
aichatdeck_tasks{status="OPEN"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "aichatdeck_tasks"); err != nil {
		t.Fatal(err)
	}
}
