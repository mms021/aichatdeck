package evals

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"aichatdeck/internal/db"
	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
)

// MemoryEntry is one line of evals/retrieval/corpus.jsonl.
type MemoryEntry struct {
	ID       string         `json:"id"`
	Category string         `json:"category"`
	Content  map[string]any `json:"content"`
	Tags     []string       `json:"tags"`
}

// RetrievalQuery is one line of evals/retrieval/queries.jsonl; Relevant are
// corpus IDs a good search must return.
type RetrievalQuery struct {
	ID       string   `json:"id"`
	Query    string   `json:"query"`
	Relevant []string `json:"relevant"`
	Tags     []string `json:"tags"`
	Source   string   `json:"source"`
}

func loadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for line := 1; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func LoadRetrieval(corpusPath, queriesPath string) ([]MemoryEntry, []RetrievalQuery, error) {
	corpus, err := loadJSONL[MemoryEntry](corpusPath)
	if err != nil {
		return nil, nil, err
	}
	queries, err := loadJSONL[RetrievalQuery](queriesPath)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]bool{}
	for _, e := range corpus {
		known[e.ID] = true
	}
	for _, q := range queries {
		for _, r := range q.Relevant {
			if !known[r] {
				return nil, nil, fmt.Errorf("query %s: relevant %s is not in the corpus", q.ID, r)
			}
		}
	}
	return corpus, queries, nil
}

// RetrievalRow is one query's ranked result.
type RetrievalRow struct {
	QueryID   string   `json:"query_id"`
	Query     string   `json:"query"`
	Tags      []string `json:"tags"`
	Relevant  []string `json:"relevant"`
	Returned  []string `json:"returned"` // corpus IDs in rank order (top 10)
	FirstRank int      `json:"first_relevant_rank"`
	Recall1   float64  `json:"recall_at_1"`
	Recall3   float64  `json:"recall_at_3"`
	Recall5   float64  `json:"recall_at_5"`
	RR        float64  `json:"reciprocal_rank"`
	LatencyMS float64  `json:"latency_ms"`
}

const retrievalDepth = 10

// RunRetrieval loads corpus into an empty database through the knowledge
// store and asks every query through KnowledgeStore.Search: the same code
// path GET /memory/search uses.
func RunRetrieval(ctx context.Context, d *db.DB, corpus []MemoryEntry, queries []RetrievalQuery) ([]RetrievalRow, error) {
	author, err := store.NewAgentStore(d).Create(ctx, model.RegisterRequest{})
	if err != nil {
		return nil, err
	}
	knowledge := store.NewKnowledgeStore(d)
	corpusID := map[string]string{}
	for _, e := range corpus {
		k, err := knowledge.Create(ctx, author.AgentID, model.CreateKnowledgeRequest{
			Category: e.Category, Content: e.Content, Tags: e.Tags, Confidence: 0.9,
		})
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", e.ID, err)
		}
		corpusID[k.KnowledgeID] = e.ID
	}
	var rows []RetrievalRow
	for _, q := range queries {
		start := time.Now()
		found, _, err := knowledge.Search(ctx, author.AgentID, model.KnowledgeFilter{Query: q.Query, Limit: retrievalDepth})
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.ID, err)
		}
		row := RetrievalRow{QueryID: q.ID, Query: q.Query, Tags: q.Tags, Relevant: q.Relevant,
			Returned: []string{}, LatencyMS: float64(time.Since(start).Microseconds()) / 1000}
		for _, k := range found {
			row.Returned = append(row.Returned, corpusID[k.KnowledgeID])
		}
		scoreRanking(&row)
		rows = append(rows, row)
	}
	return rows, nil
}

func scoreRanking(r *RetrievalRow) {
	relevant := map[string]bool{}
	for _, id := range r.Relevant {
		relevant[id] = true
	}
	recallAt := func(k int) float64 {
		hits := 0
		for i, id := range r.Returned {
			if i < k && relevant[id] {
				hits++
			}
		}
		// Normalize by what fits in k: a query with 4 relevant entries cannot
		// have more than 1 of them in the top 1.
		return float64(hits) / float64(min(k, len(r.Relevant)))
	}
	r.Recall1, r.Recall3, r.Recall5 = recallAt(1), recallAt(3), recallAt(5)
	for i, id := range r.Returned {
		if relevant[id] {
			r.FirstRank = i + 1
			r.RR = 1 / float64(i+1)
			break
		}
	}
}

type RetrievalSummary struct {
	RunID       string            `json:"run_id"`
	Label       string            `json:"label"`
	Queries     int               `json:"queries"`
	Corpus      int               `json:"corpus"`
	Recall1     Metric            `json:"recall_at_1"`
	Recall3     Metric            `json:"recall_at_3"`
	Recall5     Metric            `json:"recall_at_5"`
	MRR         Metric            `json:"mrr_at_10"`
	ZeroResults Metric            `json:"zero_result_rate"`
	ByTag       map[string]Metric `json:"recall_at_5_by_tag"`
	LatencyP95  float64           `json:"latency_p95_ms"`
}

func SummarizeRetrieval(runID, label string, corpus int, rows []RetrievalRow) RetrievalSummary {
	s := RetrievalSummary{RunID: runID, Label: label, Queries: len(rows), Corpus: corpus, ByTag: map[string]Metric{}}
	var r1, r3, r5, rr, zero, lat []float64
	byTag := map[string][]float64{}
	for _, r := range rows {
		r1, r3, r5, rr = append(r1, r.Recall1), append(r3, r.Recall3), append(r5, r.Recall5), append(rr, r.RR)
		zero = append(zero, b2f(len(r.Returned) == 0))
		lat = append(lat, r.LatencyMS)
		for _, t := range r.Tags {
			byTag[t] = append(byTag[t], r.Recall5)
		}
	}
	s.Recall1, s.Recall3, s.Recall5, s.MRR, s.ZeroResults = proportion(r1), proportion(r3), proportion(r5), proportion(rr), proportion(zero)
	for t, xs := range byTag {
		s.ByTag[t] = proportion(xs)
	}
	s.LatencyP95 = quantile(lat, 0.95)
	return s
}

func (s RetrievalSummary) Markdown(rows []RetrievalRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Memory retrieval eval — %s\n\n", s.Label)
	fmt.Fprintf(&b, "Run `%s` · %d queries over %d entries · GET /memory/search path (KnowledgeStore.Search), top %d\n\n",
		s.RunID, s.Queries, s.Corpus, retrievalDepth)
	b.WriteString("| Metric | Value (95% CI, bootstrap over queries) |\n|---|---|\n")
	fmt.Fprintf(&b, "| Recall@1 | %s |\n| Recall@3 | %s |\n| **Recall@5** | %s |\n| MRR@10 | %s |\n| Zero-result rate (lower is better) | %s |\n| Latency p95 | %.1f ms |\n",
		s.Recall1, s.Recall3, s.Recall5, s.MRR, s.ZeroResults, s.LatencyP95)
	tags := make([]string, 0, len(s.ByTag))
	for t := range s.ByTag {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	b.WriteString("\n## Recall@5 by query type\n\n| Type | Recall@5 |\n|---|---|\n")
	for _, t := range tags {
		fmt.Fprintf(&b, "| %s | %s |\n", t, s.ByTag[t])
	}
	b.WriteString("\n## Queries that missed the top 5\n\n")
	misses := 0
	for _, r := range rows {
		if r.Recall5 == 1 {
			continue
		}
		misses++
		top := r.Returned
		if len(top) > 3 {
			top = top[:3]
		}
		fmt.Fprintf(&b, "- `%s` %q: want %s, got %s\n", r.QueryID, r.Query, strings.Join(r.Relevant, ", "), orNone(top))
	}
	if misses == 0 {
		b.WriteString("None.\n")
	}
	fmt.Fprintf(&b, "\n_Generated %s._\n", time.Now().UTC().Format(time.RFC3339))
	return b.String()
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "nothing"
	}
	return strings.Join(xs, ", ")
}

// RetrievalExportItems maps a retrieval run onto experiment items.
func RetrievalExportItems(rows []RetrievalRow, at time.Time) []ExportItem {
	var items []ExportItem
	for _, r := range rows {
		items = append(items, ExportItem{
			ItemID: r.QueryID, Name: "retrieval/" + r.QueryID,
			Input: map[string]string{"query": r.Query}, Output: r.Returned, Expected: r.Relevant, Tags: r.Tags,
			Start: at, End: at.Add(time.Duration(r.LatencyMS * float64(time.Millisecond))),
			Scores: []Score{
				{Name: "recall_at_5", Value: r.Recall5, DataType: "NUMERIC"},
				{Name: "reciprocal_rank", Value: r.RR, DataType: "NUMERIC"},
			},
		})
	}
	return items
}
