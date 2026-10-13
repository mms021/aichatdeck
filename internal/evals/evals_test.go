package evals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/db"
	"aichatdeck/internal/db/dbtest"
)

func loadCases(t *testing.T) []VerifierCase {
	t.Helper()
	cases, err := LoadVerifierCases("../../evals/verifier/cases.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestOracleScoresPerfectlyThroughHarness(t *testing.T) {
	cases := loadCases(t)
	rows, errs := RunVerifier(context.Background(), OracleJudge{Cases: cases}, cases, VerifierConfig{Reps: 2, Parallel: 3})
	s := SummarizeVerifier("test", "oracle", cases, 2, rows, errs)
	if len(errs) != 0 || s.Accuracy.Value != 1 || s.FalseVerified.Value != 0 || s.GradedRows != 2*len(cases) {
		t.Fatalf("oracle must be perfect: %+v errs=%v", s, errs)
	}
}

func TestConstantVerifiedIsCaughtAsUnsafe(t *testing.T) {
	cases := loadCases(t)
	rows, _ := RunVerifier(context.Background(), ConstantJudge{Verdict: "verified"}, cases, VerifierConfig{Reps: 1})
	s := SummarizeVerifier("test", "always-verified", cases, 1, rows, nil)
	if s.FalseVerified.Value != 1 || s.InjectionResistance.Value != 0 {
		t.Fatalf("always-verified must fail every safety metric: %+v", s)
	}
}

func TestTruncatedAndRefusedRowsAreNotGraded(t *testing.T) {
	c := VerifierCase{ID: "x", Acceptable: []string{"verified"}}
	rows := []VerifierRow{
		{CaseID: "x", Rep: 1, Acceptable: c.Acceptable, Verdict: "inconclusive", Status: "truncated"},
		{CaseID: "x", Rep: 2, Acceptable: c.Acceptable, Verdict: "inconclusive", Status: "refusal"},
	}
	s := SummarizeVerifier("test", "j", []VerifierCase{c}, 2, rows, nil)
	if s.GradedRows != 0 || s.Truncated != 1 || s.Refusals != 1 || s.Accuracy.Cases != 0 {
		t.Fatalf("infra outcomes leaked into grades: %+v", s)
	}
}

func TestCostUsesExactModel(t *testing.T) {
	// Prefix matching once priced claude-opus-5-5 as claude-opus-5.
	if got := Cost("claude-opus-5-5", 1_000_000, 1_000_000, 0, 0); got != 24 {
		t.Fatalf("opus 5.5 cost = %v, want 24", got)
	}
	if Cost("claude-unknown", 1, 1, 0, 0) != 0 || Priced("claude-unknown") {
		t.Fatal("unknown models must not be guessed")
	}
}

func TestScoreRanking(t *testing.T) {
	r := RetrievalRow{Relevant: []string{"a", "b"}, Returned: []string{"x", "a", "y", "b"}}
	scoreRanking(&r)
	if r.Recall1 != 0 || r.Recall3 != 0.5 || r.Recall5 != 1 || r.RR != 0.5 || r.FirstRank != 2 {
		t.Fatalf("unexpected scores: %+v", r)
	}
}

func TestLangfuseExportShape(t *testing.T) {
	var mu sync.Mutex
	var traces map[string]any
	var scores []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "pk-lf-test" || pass != "sk-lf-test" {
			t.Errorf("bad auth on %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/public/otel/v1/traces":
			if r.Header.Get("x-langfuse-ingestion-version") != "4" {
				t.Error("missing ingestion version header")
			}
			_ = json.Unmarshal(body, &traces)
		case "/api/public/scores":
			var s map[string]any
			_ = json.Unmarshal(body, &s)
			scores = append(scores, s)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cases := loadCases(t)[:1]
	now := time.Now()
	rows := []VerifierRow{{CaseID: cases[0].ID, Rep: 1, Acceptable: cases[0].Acceptable, Verdict: "verified", Correct: true,
		Status: "ok", StartedAt: now, Call: &agent.Call{Model: "claude-opus-5-5", Prompt: "p", Output: "VERIFIED", InputTokens: 10, OutputTokens: 5, Latency: time.Second}}}
	lf := Langfuse{Host: srv.URL, PublicKey: "pk-lf-test", SecretKey: "sk-lf-test"}
	if err := lf.Export(context.Background(), Experiment{ID: "run-1", Name: "verifier/test", DatasetID: "ds"}, VerifierExportItems(cases, rows)); err != nil {
		t.Fatal(err)
	}

	spans := traces["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	if len(spans) != 2 {
		t.Fatalf("want root + generation span, got %d", len(spans))
	}
	root, gen := spans[0].(map[string]any), spans[1].(map[string]any)
	attrs := func(s map[string]any) map[string]string {
		out := map[string]string{}
		for _, a := range s["attributes"].([]any) {
			kv := a.(map[string]any)
			if v, ok := kv["value"].(map[string]any)["stringValue"].(string); ok {
				out[kv["key"].(string)] = v
			}
		}
		return out
	}
	ra, ga := attrs(root), attrs(gen)
	if len(root["traceId"].(string)) != 32 || len(root["spanId"].(string)) != 16 || gen["parentSpanId"] != root["spanId"] {
		t.Fatalf("bad ids: root=%v gen=%v", root, gen)
	}
	if ra["langfuse.experiment.item.root_observation_id"] != root["spanId"] || ra["langfuse.experiment.item.id"] != cases[0].ID {
		t.Fatalf("root experiment attributes: %v", ra)
	}
	if ga["langfuse.observation.type"] != "generation" || ga["langfuse.observation.model.name"] != "claude-opus-5-5" ||
		!strings.Contains(ga["langfuse.observation.usage_details"], `"input":10`) {
		t.Fatalf("generation attributes: %v", ga)
	}
	if _, leaked := ra["langfuse.observation.type"]; leaked {
		t.Fatal("generation attributes leaked into the root span")
	}
	if len(scores) < 2 || scores[0]["observationId"] != root["spanId"] || scores[0]["traceId"] != root["traceId"] {
		t.Fatalf("scores must attach to the root observation: %v", scores)
	}
}

// TestMemorySearchQuality is a regression gate on the retrieval eval: a
// change that makes shared memory harder to find fails CI.
func TestMemorySearchQuality(t *testing.T) {
	d, err := db.Open(dbtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	corpus, queries, err := LoadRetrieval("../../evals/retrieval/corpus.jsonl", "../../evals/retrieval/queries.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := RunRetrieval(context.Background(), d, corpus, queries)
	if err != nil {
		t.Fatal(err)
	}
	s := SummarizeRetrieval("test", "gate", len(corpus), rows)
	if s.Recall5.Value < 0.9 || s.MRR.Value < 0.85 {
		t.Fatalf("memory search regressed: recall@5 %s, MRR %s\n%s", s.Recall5, s.MRR, s.Markdown(rows))
	}
}
