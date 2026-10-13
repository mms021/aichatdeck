package store

import (
	"context"
	"testing"

	"aichatdeck/internal/model"
)

func TestMemorySearchRanksAndStaysSafe(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	author := mustCreateAgent(t, s.Agents)
	k := NewKnowledgeStore(s.Tasks.db)
	for _, summary := range []string{
		"Очередь верификации использует SKIP LOCKED",
		"Верификатор ставит inconclusive без доказательств",
		"Метрики отдаются на отдельном порту",
	} {
		if _, err := k.Create(ctx, author.AgentID, model.CreateKnowledgeRequest{Category: "decision", Content: map[string]any{"summary": summary}}); err != nil {
			t.Fatal(err)
		}
	}
	search := func(q string) []model.Knowledge {
		t.Helper()
		out, _, err := k.Search(ctx, author.AgentID, model.KnowledgeFilter{Query: q})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		return out
	}
	// An inflected form finds the entry (stemming) and ranks it first.
	if got := search("очереди верификаций"); len(got) == 0 || got[0].Content["summary"] != "Очередь верификации использует SKIP LOCKED" {
		t.Fatalf("ranked search: %+v", got)
	}
	// tsquery operators in user text are data, not syntax.
	for _, q := range []string{"a & !b | c:*", "'); DROP TABLE knowledge; --", "<->"} {
		search(q)
	}
	// No letters or digits: plain substring matching still applies.
	if got := search("<->"); len(got) != 0 {
		t.Fatalf("punctuation query matched %d entries", len(got))
	}
	_, _, err := k.Search(ctx, author.AgentID, model.KnowledgeFilter{Query: "метрики", Cursor: encodeSeqCursor(1)})
	if code := apiCode(t, err); code != model.ErrValidationError {
		t.Fatalf("ranked search with cursor: %s", code)
	}
}
