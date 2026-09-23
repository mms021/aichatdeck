package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/bus"
	"aichatdeck/internal/db"
	"aichatdeck/internal/httpapi"
	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	a, b := mcp.NewInMemoryTransports()
	ss, err := s.Connect(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}
func call(t *testing.T, c *mcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	out, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || out.IsError {
		t.Fatalf("%s: %+v %v", name, out, err)
	}
	data, _ := json.Marshal(out.StructuredContent)
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestMCPCoreLoopUsesPlatformAuthorization(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	credentials := store.NewCredentialStore(database)
	h := httptest.NewServer(httpapi.NewRouter(&httpapi.Server{DB: database, Agents: store.NewAgentStore(database), Credentials: credentials, Tasks: store.NewTaskStore(database), Messages: store.NewMessageStore(database), Knowledge: store.NewKnowledgeStore(database), Artifacts: store.NewArtifactStore(database), Projects: store.NewProjectStore(database), Members: store.NewMembershipStore(database), Bus: bus.NewFake()}))
	defer h.Close()
	ctx := context.Background()
	creator, worker := agent.NewClient(h.URL), agent.NewClient(h.URL)
	for _, c := range []*agent.Client{creator, worker} {
		if err := c.Register(ctx, model.RegisterRequest{Capabilities: []model.Capability{{Name: "testing"}}}); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := New(h.URL, creator.AgentID, creator.Token)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := New(h.URL, worker.AgentID, worker.Token)
	if err != nil {
		t.Fatal(err)
	}
	c, w := connect(t, cs), connect(t, ws)
	profile := call(t, c, "get_agent", map[string]any{"agent_id": worker.AgentID})
	if profile["availability"] != nil || profile["metadata"] != nil || profile["constraints"] != nil {
		t.Fatalf("unexpected public profile: %+v", profile)
	}
	call(t, w, "update_my_availability", map[string]any{"state": "available", "load": 0.25, "free_slots": 2, "max_parallel": 3, "budget_state": "unknown"})
	profile = call(t, c, "get_agent", map[string]any{"agent_id": worker.AgentID})
	availability := profile["availability"].(map[string]any)
	if availability["free_slots"] != float64(2) || availability["stale"] != false || availability["source"] != "self_reported" {
		t.Fatalf("availability: %+v", availability)
	}
	if own := call(t, c, "get_agent", map[string]any{"agent_id": creator.AgentID}); own["availability"] != nil {
		t.Fatal("worker changed another agent's availability")
	}
	invalid, err := w.CallTool(ctx, &mcp.CallToolParams{Name: "update_my_availability", Arguments: map[string]any{"state": "offline", "load": 0, "free_slots": 1, "max_parallel": 1, "budget_state": "unknown"}})
	if err != nil || !invalid.IsError {
		t.Fatalf("invalid availability accepted: %+v %v", invalid, err)
	}
	first := call(t, c, "list_agents", map[string]any{"capability": "testing", "limit": 1})
	second := call(t, c, "list_agents", map[string]any{"capability": "testing", "limit": 1, "cursor": first["next_cursor"]})
	if len(first["agents"].([]any)) != 1 || len(second["agents"].([]any)) != 1 || second["next_cursor"] != "" {
		t.Fatalf("agent pagination: %+v %+v", first, second)
	}
	limits := call(t, w, "get_my_limits", map[string]any{})
	if limits["limit"] != float64(300) || limits["_platform_limits"] == nil {
		t.Fatalf("missing platform limits: %+v", limits)
	}
	project, err := creator.CreateProject(ctx, model.CreateProjectRequest{Name: "MCP project"})
	if err != nil {
		t.Fatal(err)
	}
	call(t, c, "bind_project_repository", map[string]any{"project_id": project.ProjectID, "repository": "git@github.com:example/mcp-project.git"})
	found := call(t, w, "find_project", map[string]any{"repository": "https://github.com/example/mcp-project.git"})
	if len(found["projects"].([]any)) != 1 {
		t.Fatalf("project repository discovery: %+v", found)
	}
	connected := call(t, w, "connect_to_project", map[string]any{"project_id": project.ProjectID})
	if connected["connection"] != "awaiting_assignment" {
		t.Fatalf("project connection: %+v", connected)
	}
	orchestrator := agent.NewClient(h.URL)
	if err := orchestrator.Register(ctx, model.RegisterRequest{Capabilities: []model.Capability{{Name: "coordination"}}}); err != nil {
		t.Fatal(err)
	}
	os, err := New(h.URL, orchestrator.AgentID, orchestrator.Token)
	if err != nil {
		t.Fatal(err)
	}
	o := connect(t, os)
	call(t, c, "appoint_orchestrator", map[string]any{"project_id": project.ProjectID, "agent_id": orchestrator.AgentID})
	queue := call(t, o, "list_assignment_queue", map[string]any{"project_id": project.ProjectID})
	if len(queue["participants"].([]any)) != 1 {
		t.Fatalf("queue: %+v", queue)
	}
	lease := call(t, o, "manage_leadership", map[string]any{"project_id": project.ProjectID, "generation": 0})
	call(t, o, "save_checkpoint", map[string]any{"project_id": project.ProjectID, "generation": lease["generation"], "progress": map[string]any{"next": "test registration"}})
	call(t, o, "assign_work_role", map[string]any{"project_id": project.ProjectID, "agent_id": worker.AgentID, "work_role": "tester", "generation": lease["generation"]})
	call(t, o, "manage_leadership", map[string]any{"project_id": project.ProjectID, "generation": lease["generation"], "target_agent_id": creator.AgentID})
	stale, e := o.CallTool(ctx, &mcp.CallToolParams{Name: "assign_work_role", Arguments: map[string]any{"project_id": project.ProjectID, "agent_id": worker.AgentID, "work_role": "developer", "generation": lease["generation"]}})
	if e != nil || !stale.IsError {
		t.Fatal("old leader retained authority")
	}
	progress := call(t, c, "get_project_progress", map[string]any{"project_id": project.ProjectID})
	if len(progress["checkpoints"].([]any)) != 1 {
		t.Fatal("handoff lost checkpoint")
	}
	assignment := call(t, w, "get_my_assignment", map[string]any{"project_id": project.ProjectID})
	if assignment["connection"] != "ready" {
		t.Fatalf("assignment: %+v", assignment)
	}
	again := call(t, w, "connect_to_project", map[string]any{"project_id": project.ProjectID})
	if again["membership"].(map[string]any)["work_role"] != "tester" {
		t.Fatal("reconnect reset role")
	}
	deniedAppointment, e := o.CallTool(ctx, &mcp.CallToolParams{Name: "appoint_orchestrator", Arguments: map[string]any{"project_id": project.ProjectID, "agent_id": worker.AgentID}})
	if e != nil || !deniedAppointment.IsError {
		t.Fatal("orchestrator escalated authority")
	}
	untyped := call(t, c, "create_task", map[string]any{"project_id": project.ProjectID, "objective": "untyped"})
	deniedUntyped, e := w.CallTool(ctx, &mcp.CallToolParams{Name: "claim_task", Arguments: map[string]any{"task_id": untyped["task_id"]}})
	if e != nil || !deniedUntyped.IsError {
		t.Fatal("tester claimed unrestricted task")
	}
	other, err := creator.CreateProject(ctx, model.CreateProjectRequest{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	deniedOther, e := o.CallTool(ctx, &mcp.CallToolParams{Name: "assign_work_role", Arguments: map[string]any{"project_id": other.ProjectID, "agent_id": worker.AgentID, "work_role": "developer"}})
	if e != nil || !deniedOther.IsError {
		t.Fatal("cross-project authority")
	}
	if got := call(t, w, "get_project", map[string]any{"project_id": project.ProjectID}); got["project_id"] != project.ProjectID {
		t.Fatalf("project lookup: %+v", got)
	}
	call(t, c, "assign_work_role", map[string]any{"project_id": project.ProjectID, "agent_id": worker.AgentID, "work_role": "tester"})
	restricted := call(t, c, "create_task", map[string]any{"objective": "test-only task", "project_id": project.ProjectID, "allowed_work_roles": []string{"tester"}})
	denied, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "claim_task", Arguments: map[string]any{"task_id": restricted["task_id"]}})
	if err != nil || !denied.IsError {
		t.Fatalf("unassigned owner claimed restricted task: %+v %v", denied, err)
	}
	call(t, w, "claim_task", map[string]any{"task_id": restricted["task_id"]})
	memory := call(t, w, "publish_memory", map[string]any{"category": "lesson", "content": map[string]any{"summary": "MCP lesson"}})
	if memory["status"] != "proposed" || memory["author"] != worker.AgentID {
		t.Fatalf("memory identity/status: %+v", memory)
	}
	list, err := w.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 30 {
		t.Fatalf("tool count=%d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if strings.Contains(tool.Name, "revoke") || strings.Contains(tool.Name, "credential") {
			t.Fatal("administrative tool exposed")
		}
	}
	task := call(t, c, "create_task", map[string]any{"objective": "MCP task"})
	id := task["task_id"].(string)
	page := call(t, w, "list_tasks", map[string]any{"status": "OPEN", "limit": 1, "project_id": ""})
	if len(page["tasks"].([]any)) != 1 {
		t.Fatalf("missing task: %+v", page)
	}
	claim := call(t, w, "claim_task", map[string]any{"task_id": id})
	question := map[string]any{"message_id": "mcp-question", "recipient": creator.AgentID, "type": "QUESTION", "text": "Expected output?", "task_id": id}
	call(t, w, "send_message", question)
	call(t, w, "send_message", question)
	questions := call(t, c, "get_messages", map[string]any{"types": []string{"QUESTION"}})
	if len(questions["messages"].([]any)) != 1 {
		t.Fatal("question deduplication failed")
	}
	answer := call(t, c, "send_message", map[string]any{"message_id": "mcp-answer", "recipient": worker.AgentID, "type": "ANSWER", "text": "A report", "task_id": id, "reply_to": "mcp-question"})
	if answer["sender"] != creator.AgentID {
		t.Fatal("incorrect answer sender")
	}
	call(t, w, "heartbeat_task", map[string]any{"task_id": id, "claim_id": claim["claim_id"]})
	args := map[string]any{"task_id": id, "claim_id": claim["claim_id"], "message_id": "mcp-result", "summary": "done", "status": "success"}
	result := call(t, w, "submit_result", args)
	if result["sender"] != worker.AgentID || result["recipient"] != creator.AgentID {
		t.Fatal("wrong result identity")
	}
	call(t, w, "submit_result", args) // HTTP message dedup is retained
	inbox := call(t, c, "get_messages", map[string]any{"types": []string{"RESULT"}})
	if len(inbox["messages"].([]any)) != 1 {
		t.Fatal("result duplicated")
	}
	call(t, c, "get_messages", map[string]any{"cursor": inbox["next_cursor"], "types": []string{"RESULT"}})
	if job := call(t, w, "claim_verification", map[string]any{})["job"]; job != nil {
		t.Fatal("worker obtained creator verification")
	}
	job := call(t, c, "claim_verification", map[string]any{})["job"].(map[string]any)
	verified := call(t, c, "verify_result", map[string]any{"task_id": id, "attempt_id": job["attempt_id"], "target_message_id": "mcp-result", "verdict": "verified"})
	if verified["task"].(map[string]any)["status"] != "COMPLETED" {
		t.Fatal("task not completed")
	}
	if err := credentials.RevokeCurrent(ctx, worker.Token); err != nil {
		t.Fatal(err)
	}
	out, err := w.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil || !out.IsError {
		t.Fatalf("revoked credential accepted: %+v %v", out, err)
	}
}

func TestMCPRejectsPathInjectionAndDoesNotFollowRedirects(t *testing.T) {
	hits := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; t.Error("redirect target reached") }))
	defer destination.Close()
	requests := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.Redirect(w, r, destination.URL, 302) }))
	defer h.Close()
	s, err := New(h.URL, "agent", "secret-fixture")
	if err != nil {
		t.Fatal(err)
	}
	client := connect(t, s)
	for _, id := range []string{"../credentials/revoke", "x?token=secret", "%2fadmin"} {
		out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "claim_task", Arguments: map[string]any{"task_id": id}})
		if err != nil || !out.IsError {
			t.Fatalf("unsafe id: %s %v", id, err)
		}
	}
	if requests != 0 {
		t.Fatal("invalid IDs reached API")
	}
	out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_task", Arguments: map[string]any{"task_id": "task-1"}})
	if err != nil || !out.IsError || hits != 0 {
		t.Fatalf("redirect handling: %+v %v", out, err)
	}
	if strings.Contains(fmt.Sprint(out.Content), "secret-fixture") {
		t.Fatal("credential in tool error")
	}
}

func TestMCPRejectsUnsafeConfiguration(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?key=x", "https://example.com/other", "file:///tmp/server"} {
		if _, err := New(base, "agent", "token"); err == nil {
			t.Errorf("accepted %s", base)
		}
	}
}

func TestMCPRateLimitErrorRetainsReset(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "300")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1900000000")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"private":"must not leak"}`)
	}))
	defer h.Close()
	s, err := New(h.URL, "agent", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	out, err := connect(t, s).CallTool(context.Background(), &mcp.CallToolParams{Name: "get_my_limits", Arguments: map[string]any{}})
	if err != nil || !out.IsError {
		t.Fatalf("expected rate error: %+v %v", out, err)
	}
	data, _ := json.Marshal(out)
	for _, expected := range []string{"429", "remaining=0", "reset_at=1900000000"} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("missing %s: %s", expected, data)
		}
	}
	if strings.Contains(string(data), "must not leak") {
		t.Fatal("backend body leaked")
	}
}

func TestMCPForwardsArgumentsAndExplicitCursors(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, queryKey, queryValue string
		args                                     map[string]any
	}{
		{"list_tasks", "GET", "/tasks", "cursor", "page-2", map[string]any{"cursor": "page-2", "project_id": "private", "tools": []string{"shell"}}},
		{"get_messages", "GET", "/messages", "cursor", "message-page", map[string]any{"cursor": "message-page", "types": []string{"ANSWER"}}},
		{"search_memory", "GET", "/memory/search", "q", "a&b", map[string]any{"query": "a&b", "project_id": "private"}},
		{"get_artifact", "GET", "/artifacts/artifact-1", "", "", map[string]any{"artifact_id": "artifact-1"}},
		{"register_artifact", "POST", "/artifacts", "", "", map[string]any{"type": "log", "uri": "https://example.com/report.txt", "checksum": "sha256:fixture"}},
		{"request_operator", "POST", "/operator/requests", "", "", map[string]any{"kind": "wish", "title": "improvement", "details": "details"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Errorf("wrong forwarding: %s %s", r.Method, r.URL.Path)
				}
				if tc.queryKey != "" && r.URL.Query().Get(tc.queryKey) != tc.queryValue {
					t.Errorf("query not preserved: %s", r.URL.RawQuery)
				}
				if tc.method == "POST" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					for key, value := range tc.args {
						if body[key] != value {
							t.Errorf("body field %s not preserved", key)
						}
					}
				}
				fmt.Fprint(w, `{"next_cursor":"next-page"}`)
			}))
			defer h.Close()
			s, err := New(h.URL, "agent", "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			if out := call(t, connect(t, s), tc.name, tc.args); out["next_cursor"] != "next-page" {
				t.Fatal("cursor dropped")
			}
		})
	}
}
