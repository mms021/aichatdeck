package evals

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Langfuse exports an eval run as a Langfuse experiment: one trace per
// (case, rep) sent as OTLP/HTTP JSON to /api/public/otel/v1/traces, with
// langfuse.experiment.* attributes linking it to a local dataset item, and
// scores posted to /api/public/scores against each trace's root span.
//
// The legacy /api/public/ingestion batch endpoint is deliberately not used:
// Langfuse Cloud shuts it down on 2026-11-16 (score-create aside).
type Langfuse struct {
	Host      string // e.g. https://cloud.langfuse.com
	PublicKey string
	SecretKey string
	Client    *http.Client
}

// ExportItem is one experiment item trace.
type ExportItem struct {
	ItemID     string // stable dataset item ID (the case or query ID)
	Name       string
	Input      any
	Output     any
	Expected   any
	Tags       []string
	Metadata   map[string]string
	Start, End time.Time
	Generation *Generation // the model call, when there was one
	Scores     []Score

	traceID, rootSpanID string
}

type Generation struct {
	Model   string
	Prompt  string
	Output  string
	Usage   map[string]int64
	CostUSD float64
}

type Score struct {
	Name     string
	Value    any    // float64 for NUMERIC/BOOLEAN (0/1), string for CATEGORICAL
	DataType string // NUMERIC | BOOLEAN | CATEGORICAL
	Comment  string
}

// Experiment identifies the run and the local dataset it ran over.
type Experiment struct {
	ID, Name, Description, DatasetID string
}

func (l Langfuse) Export(ctx context.Context, exp Experiment, items []ExportItem) error {
	for i := range items {
		items[i].traceID, items[i].rootSpanID = randomHex(16), randomHex(8)
	}
	const perRequest = 50
	for start := 0; start < len(items); start += perRequest {
		end := min(start+perRequest, len(items))
		if err := l.post(ctx, "/api/public/otel/v1/traces", otlpBody(exp, items[start:end]),
			map[string]string{"x-langfuse-ingestion-version": "4"}); err != nil {
			return fmt.Errorf("export traces: %w", err)
		}
	}
	for _, it := range items {
		for _, s := range it.Scores {
			body := map[string]any{"traceId": it.traceID, "observationId": it.rootSpanID,
				"name": s.Name, "value": s.Value, "dataType": s.DataType}
			if s.Comment != "" {
				body["comment"] = s.Comment
			}
			if err := l.post(ctx, "/api/public/scores", body, nil); err != nil {
				return fmt.Errorf("export score %s for %s: %w", s.Name, it.ItemID, err)
			}
		}
	}
	return nil
}

func (l Langfuse) post(ctx context.Context, path string, body any, headers map[string]string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.Host, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.SetBasicAuth(l.PublicKey, l.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// otlpBody builds an OTLP ExportTraceServiceRequest in its JSON encoding
// (hex trace/span IDs, nanosecond timestamps as strings).
func otlpBody(exp Experiment, items []ExportItem) map[string]any {
	var spans []map[string]any
	for _, it := range items {
		shared := []map[string]any{
			attr("langfuse.experiment.id", exp.ID),
			attr("langfuse.experiment.name", exp.Name),
			attr("langfuse.experiment.dataset.id", exp.DatasetID),
			attr("langfuse.experiment.item.id", it.ItemID),
			attr("langfuse.environment", "eval"),
		}
		root := slices.Concat(shared, []map[string]any{
			attr("langfuse.trace.name", it.Name),
			attr("langfuse.session.id", exp.ID),
			attr("langfuse.trace.tags", it.Tags),
			attr("langfuse.experiment.description", exp.Description),
			attr("langfuse.experiment.item.root_observation_id", it.rootSpanID),
			attr("langfuse.experiment.item.expected_output", jsonText(it.Expected)),
			attr("langfuse.observation.input", jsonText(it.Input)),
			attr("langfuse.observation.output", jsonText(it.Output)),
		})
		for k, v := range it.Metadata {
			root = append(root, attr("langfuse.trace.metadata."+k, v))
		}
		spans = append(spans, span(it.traceID, it.rootSpanID, "", it.Name, it.Start, it.End, root))
		if g := it.Generation; g != nil {
			gen := slices.Concat(shared, []map[string]any{
				attr("langfuse.observation.type", "generation"),
				attr("langfuse.observation.model.name", g.Model),
				attr("langfuse.observation.input", g.Prompt),
				attr("langfuse.observation.output", g.Output),
				attr("langfuse.observation.usage_details", jsonText(g.Usage)),
				attr("langfuse.observation.cost_details", jsonText(map[string]float64{"total": g.CostUSD})),
			})
			spans = append(spans, span(it.traceID, randomHex(8), it.rootSpanID, "claude.messages", it.Start, it.End, gen))
		}
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{attr("service.name", "aichatdeck-evals")}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "aichatdeck/evals"}, "spans": spans}},
	}}}
}

func span(traceID, spanID, parent, name string, start, end time.Time, attrs []map[string]any) map[string]any {
	s := map[string]any{
		"traceId": traceID, "spanId": spanID, "name": name, "kind": 1,
		"startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10),
		"endTimeUnixNano":   strconv.FormatInt(end.UnixNano(), 10),
		"attributes":        attrs,
	}
	if parent != "" {
		s["parentSpanId"] = parent
	}
	return s
}

func attr(key string, value any) map[string]any {
	var v map[string]any
	switch x := value.(type) {
	case []string:
		vals := make([]any, len(x))
		for i, s := range x {
			vals[i] = map[string]any{"stringValue": s}
		}
		v = map[string]any{"arrayValue": map[string]any{"values": vals}}
	default:
		v = map[string]any{"stringValue": fmt.Sprint(x)}
	}
	return map[string]any{"key": key, "value": v}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// VerifierExportItems maps a verifier run onto experiment items.
func VerifierExportItems(cases []VerifierCase, rows []VerifierRow) []ExportItem {
	byID := map[string]VerifierCase{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	var items []ExportItem
	for _, r := range rows {
		c := byID[r.CaseID]
		it := ExportItem{
			ItemID: c.ID, Name: "verifier/" + c.ID,
			Input:    map[string]any{"task": c.Task, "result": c.Result, "artifacts": c.Artifacts},
			Output:   map[string]string{"verdict": r.Verdict, "rationale": r.Rationale},
			Expected: c.Acceptable, Tags: c.Tags,
			Metadata: map[string]string{"rep": strconv.Itoa(r.Rep), "status": r.Status},
			Start:    r.StartedAt, End: r.StartedAt,
			Scores: []Score{
				{Name: "correct", Value: b2f(r.Correct), DataType: "BOOLEAN", Comment: "verdict in " + strings.Join(c.Acceptable, "|")},
				{Name: "verdict", Value: r.Verdict, DataType: "CATEGORICAL"},
			},
		}
		if !slices.Contains(c.Acceptable, "verified") {
			it.Scores = append(it.Scores, Score{Name: "false_verified", Value: b2f(r.FalseVerified), DataType: "BOOLEAN"})
		}
		if call := r.Call; call != nil {
			it.End = r.StartedAt.Add(call.Latency)
			it.Generation = &Generation{Model: call.Model, Prompt: call.Prompt, Output: call.Output, CostUSD: r.CostUSD,
				Usage: map[string]int64{"input": call.InputTokens, "output": call.OutputTokens,
					"cache_read_input_tokens": call.CacheReadTokens, "cache_creation_input_tokens": call.CacheWriteTokens}}
		}
		items = append(items, it)
	}
	return items
}
