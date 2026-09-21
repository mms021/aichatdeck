package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"aichatdeck/internal/model"
)

// Rate limits. POST /agents/register is the one write anyone can make without
// a credential: unbounded, it is a faucet for agents and tokens, and every
// other abuse on the platform (prompt injection into other agents' work,
// memory spam) starts by getting an identity from it. RFC-1000 §8 reserves
// rate_limited for exactly this.
const (
	registerPerMinute = 30  // per client IP, unauthenticated
	agentPerMinute    = 300 // per authenticated agent, all endpoints
)

// limiter is a fixed-window counter.
// ponytail: in-memory and per-process — a second server node gets its own
// budget. Move the counters to Redis (already a dependency) if the platform
// ever runs more than one.
type limiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	counters  map[string]*counter
	nextSweep time.Time
}

type counter struct {
	n       int
	resetAt time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, counters: map[string]*counter{}}
}

// allow reports whether key is under budget, plus the budget state so callers
// can surface it (RFC-1100 §13: a limited agent needs to know when to retry,
// and every agent needs to know the shared budget shape, not just its own hit).
func (l *limiter) allow(key string) (ok bool, remaining int, resetAt time.Time) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Drop expired keys once per window: the map is keyed by client-supplied
	// identity, so letting it grow forever would be its own memory exhaustion.
	if now.After(l.nextSweep) {
		for k, c := range l.counters {
			if now.After(c.resetAt) {
				delete(l.counters, k)
			}
		}
		l.nextSweep = now.Add(l.window)
	}

	c, exists := l.counters[key]
	if !exists || now.After(c.resetAt) {
		c = &counter{n: 1, resetAt: now.Add(l.window)}
		l.counters[key] = c
		return true, l.max - c.n, c.resetAt
	}
	if c.n >= l.max {
		return false, 0, c.resetAt
	}
	c.n++
	return true, l.max - c.n, c.resetAt
}

// setLimitHeaders exposes the budget so an agent can see how much of its own
// (or, for the shared register limiter, its IP's) rate limit is left, and pace
// itself before hitting 429 instead of guessing.
func setLimitHeaders(w http.ResponseWriter, max, remaining int, resetAt time.Time) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(max))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
}

// rateLimit rejects a request over budget for its key.
func (s *Server) rateLimit(l *limiter, key func(*http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, remaining, resetAt := l.allow(key(r))
		setLimitHeaders(w, l.max, remaining, resetAt)
		if !ok {
			s.writeErr(w, model.RateLimited("rate limit exceeded, retry later"))
			return
		}
		next(w, r)
	}
}

// clientIP is the peer address only. X-Forwarded-For is deliberately ignored:
// the platform terminates connections directly, so a header a client can set
// itself would just be a free way around the limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
