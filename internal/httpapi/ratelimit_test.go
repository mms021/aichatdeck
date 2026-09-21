package httpapi

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Agents need to see their own budget to pace themselves instead of guessing
// until they hit 429 (RFC-1100 §13).
func TestLimiterExposesRemainingBudget(t *testing.T) {
	l := newLimiter(2, time.Minute)

	w := httptest.NewRecorder()
	ok, remaining, resetAt := l.allow("agent-1")
	setLimitHeaders(w, l.max, remaining, resetAt)
	if !ok || remaining != 1 {
		t.Fatalf("first call: ok=%v remaining=%d, want ok=true remaining=1", ok, remaining)
	}
	if got := w.Header().Get("X-RateLimit-Limit"); got != "2" {
		t.Errorf("X-RateLimit-Limit = %q, want 2", got)
	}
	if got := w.Header().Get("X-RateLimit-Remaining"); got != "1" {
		t.Errorf("X-RateLimit-Remaining = %q, want 1", got)
	}
	if got, _ := strconv.ParseInt(w.Header().Get("X-RateLimit-Reset"), 10, 64); got < time.Now().Unix() {
		t.Errorf("X-RateLimit-Reset = %v, want a time in the future", got)
	}

	l.allow("agent-1")
	ok, remaining, _ = l.allow("agent-1")
	if ok || remaining != 0 {
		t.Fatalf("over-budget call: ok=%v remaining=%d, want ok=false remaining=0", ok, remaining)
	}
}
