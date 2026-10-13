package httpapi_test

import (
	"net/http"
	"testing"
)

func TestCancelTask(t *testing.T) {
	srv := newTestServer(t)
	creator, worker := register(t, srv.URL), register(t, srv.URL)
	_, task := creator.do(http.MethodPost, "/tasks", map[string]any{"objective": "duplicate", "context": map[string]any{"keep": true}})
	path := "/tasks/" + task["task_id"].(string)
	body := map[string]any{"reason": "duplicate of canonical task"}
	if status, _ := worker.do(http.MethodPost, path+"/cancel", body); status != 403 {
		t.Fatalf("non-creator: %d", status)
	}
	if status, _ := creator.do(http.MethodPost, path+"/cancel", map[string]any{"reason": " "}); status != 400 {
		t.Fatalf("empty reason: %d", status)
	}
	for range 2 {
		status, out := creator.do(http.MethodPost, path+"/cancel", body)
		if status != 200 || out["status"] != "CANCELLED" {
			t.Fatalf("cancel: %d %v", status, out)
		}
		ctx := out["context"].(map[string]any)
		if ctx["keep"] != true || ctx["cancellation"].(map[string]any)["reason"] != body["reason"] {
			t.Fatalf("history lost: %v", ctx)
		}
	}
	if status, _ := worker.do(http.MethodPost, path+"/claim", nil); status != 409 {
		t.Fatalf("cancelled claim: %d", status)
	}
	_, task = creator.do(http.MethodPost, "/tasks", map[string]any{"objective": "active"})
	path = "/tasks/" + task["task_id"].(string)
	if status, _ := worker.do(http.MethodPost, path+"/claim", nil); status != 200 {
		t.Fatalf("claim: %d", status)
	}
	if status, _ := creator.do(http.MethodPost, path+"/cancel", body); status != 409 {
		t.Fatalf("active cancellation: %d", status)
	}
}
