// Package evals measures the two model-facing parts of the platform: the
// verifier (an LLM judge of other agents' results) and shared-memory retrieval.
//
// Runs are immutable directories under evals/runs/<run-id>: config.json,
// results.jsonl (one row per scored case×rep), errors.jsonl (attempts that
// produced nothing scorable — infra failures are never counted as model
// mistakes), summary.json and report.md.
package evals

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/model"
)

// VerifierCase is one line of evals/verifier/cases.jsonl. Acceptable lists
// every verdict the verifier's own rules allow; most cases have exactly one.
type VerifierCase struct {
	ID     string   `json:"id"`
	Tags   []string `json:"tags"`
	Source string   `json:"source"`
	Task   struct {
		Objective       string   `json:"objective"`
		SuccessCriteria []string `json:"success_criteria"`
		Constraints     []string `json:"constraints"`
	} `json:"task"`
	Result     model.ResultPayload `json:"result"`
	Artifacts  []CaseArtifact      `json:"artifacts,omitempty"`
	Acceptable []string            `json:"acceptable"`
	Note       string              `json:"note,omitempty"`
}

// CaseArtifact is artifact text as the agent runtime would have fetched it:
// checksum-verified, so the eval exercises the same prompt path.
type CaseArtifact struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

func LoadVerifierCases(path string) ([]VerifierCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cases []VerifierCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for line := 1; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var c VerifierCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if c.ID == "" || len(c.Acceptable) == 0 {
			return nil, fmt.Errorf("%s:%d: id and acceptable are required", path, line)
		}
		cases = append(cases, c)
	}
	return cases, sc.Err()
}

// taskAndInput builds exactly what the agent runtime passes to Brain.Verify.
func (c VerifierCase) taskAndInput() (model.Task, agent.VerificationInput) {
	task := model.Task{
		TaskID:          "task-eval-" + c.ID,
		Objective:       c.Task.Objective,
		SuccessCriteria: c.Task.SuccessCriteria,
		Constraints:     c.Task.Constraints,
		Status:          model.TaskSubmitted,
	}
	input := agent.VerificationInput{Result: c.Result}
	for _, a := range c.Artifacts {
		sum := sha256.Sum256([]byte(a.Text))
		digest := hex.EncodeToString(sum[:])
		input.Result.Artifacts = append(input.Result.Artifacts, a.ID)
		input.Artifacts = append(input.Artifacts, model.Artifact{
			ArtifactID: a.ID, SchemaVersion: "1.0", Type: a.Type, URI: "https://artifacts.example/" + a.ID + ".txt",
			Checksum: "sha256:" + digest, TaskID: &task.TaskID, Size: int64(len(a.Text)), Retention: "permanent",
		})
		input.Contents = append(input.Contents, agent.ArtifactContent{ArtifactID: a.ID, Text: a.Text, SHA256: digest})
	}
	return task, input
}

// Judge is the system under test. Call is nil for offline baselines.
type Judge interface {
	Name() string
	Verify(ctx context.Context, task model.Task, input agent.VerificationInput) (verdict, rationale string, call *agent.Call, err error)
}

// ClaudeJudge runs the production verifier (agent.ClaudeBrain.VerifyTraced).
type ClaudeJudge struct{ Brain *agent.ClaudeBrain }

func (j ClaudeJudge) Name() string { return j.Brain.Model }

func (j ClaudeJudge) Verify(ctx context.Context, task model.Task, input agent.VerificationInput) (string, string, *agent.Call, error) {
	verdict, rationale, call, err := j.Brain.VerifyTraced(ctx, task, input)
	return verdict, rationale, &call, err
}

// BrainJudge adapts any agent.Brain (EchoBrain) — no usage data.
type BrainJudge struct {
	Label string
	Brain agent.Brain
}

func (j BrainJudge) Name() string { return j.Label }

func (j BrainJudge) Verify(ctx context.Context, task model.Task, input agent.VerificationInput) (string, string, *agent.Call, error) {
	verdict, rationale, err := j.Brain.Verify(ctx, task, input)
	return verdict, rationale, nil, err
}

// ConstantJudge always answers Verdict: the null baselines that show how
// much of a score the label distribution alone explains.
type ConstantJudge struct{ Verdict string }

func (j ConstantJudge) Name() string { return "always-" + j.Verdict }

func (j ConstantJudge) Verify(context.Context, model.Task, agent.VerificationInput) (string, string, *agent.Call, error) {
	return j.Verdict, "constant baseline", nil, nil
}

// VerifierRow is one scored (case, rep).
type VerifierRow struct {
	CaseID        string      `json:"case_id"`
	Rep           int         `json:"rep"`
	Tags          []string    `json:"tags"`
	Acceptable    []string    `json:"acceptable"`
	Verdict       string      `json:"verdict"`
	Rationale     string      `json:"rationale"`
	Status        string      `json:"status"` // ok | truncated | refusal; only ok rows are graded
	Correct       bool        `json:"correct"`
	FalseVerified bool        `json:"false_verified"`
	CostUSD       float64     `json:"cost_usd"`
	StartedAt     time.Time   `json:"started_at"`
	Call          *agent.Call `json:"call,omitempty"`
}

// ErrorRow is an attempt that produced nothing scorable.
type ErrorRow struct {
	CaseID string `json:"case_id"`
	Rep    int    `json:"rep"`
	Class  string `json:"class"` // api_error | timeout | served_model_mismatch
	Error  string `json:"error"`
}

type VerifierConfig struct {
	Reps        int
	Parallel    int
	CaseTimeout time.Duration
	// WantModel, when set, must equal the model that served each call.
	WantModel string
}

// RunVerifier grades every case Reps times. It never retries a whole case:
// the SDK already retries transient API errors, and anything left goes to errors.
func RunVerifier(ctx context.Context, judge Judge, cases []VerifierCase, cfg VerifierConfig) ([]VerifierRow, []ErrorRow) {
	type job struct {
		c   VerifierCase
		rep int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var rows []VerifierRow
	var errs []ErrorRow
	var wg sync.WaitGroup
	for w := 0; w < max(cfg.Parallel, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				row, erow := gradeOne(ctx, judge, j.c, j.rep, cfg)
				mu.Lock()
				if erow != nil {
					errs = append(errs, *erow)
				} else {
					rows = append(rows, row)
				}
				mu.Unlock()
			}
		}()
	}
	for rep := 1; rep <= max(cfg.Reps, 1); rep++ {
		for _, c := range cases {
			jobs <- job{c, rep}
		}
	}
	close(jobs)
	wg.Wait()
	byCase := func(a, b VerifierRow) int {
		return strings.Compare(fmt.Sprintf("%s#%03d", a.CaseID, a.Rep), fmt.Sprintf("%s#%03d", b.CaseID, b.Rep))
	}
	slices.SortFunc(rows, byCase)
	return rows, errs
}

func gradeOne(ctx context.Context, judge Judge, c VerifierCase, rep int, cfg VerifierConfig) (VerifierRow, *ErrorRow) {
	timeout := cfg.CaseTimeout
	if timeout == 0 {
		timeout = 4 * time.Minute // the agent runtime's own verification budget
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	task, input := c.taskAndInput()
	started := time.Now().UTC()
	verdict, rationale, call, err := judge.Verify(ctx, task, input)
	if err != nil {
		class := "api_error"
		if ctx.Err() != nil {
			class = "timeout"
		}
		return VerifierRow{}, &ErrorRow{CaseID: c.ID, Rep: rep, Class: class, Error: err.Error()}
	}
	if call != nil && cfg.WantModel != "" && call.Model != cfg.WantModel {
		return VerifierRow{}, &ErrorRow{CaseID: c.ID, Rep: rep, Class: "served_model_mismatch",
			Error: fmt.Sprintf("asked for %s, served by %s", cfg.WantModel, call.Model)}
	}
	row := VerifierRow{CaseID: c.ID, Rep: rep, Tags: c.Tags, Acceptable: c.Acceptable, Verdict: verdict,
		Rationale: rationale, Status: "ok", StartedAt: started, Call: call}
	if call != nil {
		row.CostUSD = Cost(call.Model, call.InputTokens, call.OutputTokens, call.CacheReadTokens, call.CacheWriteTokens)
		switch call.StopReason {
		case "max_tokens":
			row.Status = "truncated"
		case "refusal":
			row.Status = "refusal"
		}
	}
	row.Correct = row.Status == "ok" && slices.Contains(c.Acceptable, verdict)
	row.FalseVerified = row.Status == "ok" && verdict == "verified" && !slices.Contains(c.Acceptable, "verified")
	return row, nil
}

// pricing is USD per million tokens: input, output, cache read, cache write
// (5-minute TTL, 1.25x input). Keyed by the exact model ID the API returns.
var pricing = map[string][4]float64{
	"claude-opus-5-5":   {4, 20, 0.20, 5},
	"claude-opus-5":     {5, 25, 0.50, 6.25},
	"claude-sonnet-5-5": {2, 10, 0.20, 2.50},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01, 0.125},
}

// Cost prices one call from its recorded usage. An unknown model costs 0
// and the report says so instead of guessing.
func Cost(modelID string, in, out, cacheRead, cacheWrite int64) float64 {
	p, ok := pricing[modelID]
	if !ok {
		return 0
	}
	return (float64(in)*p[0] + float64(out)*p[1] + float64(cacheRead)*p[2] + float64(cacheWrite)*p[3]) / 1e6
}

// Priced reports whether Cost knows modelID.
func Priced(modelID string) bool { _, ok := pricing[modelID]; return ok }
