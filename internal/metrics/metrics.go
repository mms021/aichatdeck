// Package metrics holds the Prometheus instruments. Counters are bumped by
// the HTTP layer, the only entry point that changes state; gauges for queue
// depth and task status are read from PostgreSQL at scrape time, so they are
// right even after a restart.
package metrics

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ns = "aichatdeck"

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "http_requests_total", Help: "HTTP requests by route pattern and status code.",
	}, []string{"method", "route", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "http_request_duration_seconds", Help: "HTTP request latency by route pattern. Long-poll GET /messages included.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"method", "route"})

	// TaskEvents counts accepted task operations: created (idempotent
	// replays included), claimed, submitted, cancelled.
	TaskEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "task_events_total", Help: "Accepted task operations.",
	}, []string{"event"})
	Verdicts = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "verifications_total", Help: "Recorded verification verdicts.",
	}, []string{"verdict"})
	Messages = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "messages_total", Help: "Newly stored messages by envelope type (replays excluded).",
	}, []string{"type"})
	Registrations = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns, Name: "agent_registrations_total", Help: "New agent identities.",
	})
	APIErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "api_errors_total", Help: "Error responses by RFC-1000 error_code.",
	}, []string{"code"})
	EventsPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "events_published_total", Help: "Outbox events acknowledged by Kafka.",
	}, []string{"type"})
	OutboxPublishErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns, Name: "outbox_publish_errors_total", Help: "Failed outbox relay rounds; events stay queued.",
	})
	LongPollWaiters = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "longpoll_waiters", Help: "GET /messages requests currently waiting for a wakeup.",
	})
)

// Middleware records rate and latency per route pattern. It must wrap the
// ServeMux: the mux fills in r.Pattern, which keeps label cardinality fixed
// no matter what IDs appear in paths.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.code)).Inc()
		httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RegisterDB adds connection-pool stats and the scrape-time state gauges.
func RegisterDB(db *sql.DB) {
	prometheus.MustRegister(collectors.NewDBStatsCollector(db, ns), &stateCollector{db: db})
}

// Handler serves the default registry (Go runtime, process, and the above).
func Handler() http.Handler { return promhttp.Handler() }

var (
	tasksDesc         = prometheus.NewDesc(ns+"_tasks", "Tasks by current status.", []string{"status"}, nil)
	outboxPendingDesc = prometheus.NewDesc(ns+"_outbox_pending", "Task events not yet acknowledged by Kafka.", nil, nil)
	outboxAgeDesc     = prometheus.NewDesc(ns+"_outbox_oldest_pending_seconds", "Age of the oldest unpublished event (relay lag).", nil, nil)
	queueDesc         = prometheus.NewDesc(ns+"_verification_queue", "Unfinished verification jobs: due, leased, waiting (backoff) or exhausted.", []string{"state"}, nil)
)

type stateCollector struct{ db *sql.DB }

func (c *stateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- tasksDesc
	ch <- queueDesc
	ch <- outboxPendingDesc
	ch <- outboxAgeDesc
}

func (c *stateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.collect(ctx, ch, tasksDesc, `SELECT status, count(*) FROM tasks GROUP BY status`)
	// Mirrors the claim and exhaustion conditions in store/verification_*.go.
	c.collect(ctx, ch, queueDesc, `SELECT CASE
			WHEN attempts >= 5 AND lease_until <= now() THEN 'exhausted'
			WHEN lease_until > now() THEN 'leased'
			WHEN next_at <= now() THEN 'due'
			ELSE 'waiting' END AS state, count(*)
		FROM verification_jobs WHERE done = 0 GROUP BY 1`)
	var pending, age float64
	if err := c.db.QueryRowContext(ctx, `SELECT count(*), COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)
		FROM outbox WHERE published_at IS NULL`).Scan(&pending, &age); err != nil {
		ch <- prometheus.NewInvalidMetric(outboxPendingDesc, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(outboxPendingDesc, prometheus.GaugeValue, pending)
	ch <- prometheus.MustNewConstMetric(outboxAgeDesc, prometheus.GaugeValue, age)
}

func (c *stateCollector) collect(ctx context.Context, ch chan<- prometheus.Metric, desc *prometheus.Desc, query string) {
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(desc, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var n float64
		if err := rows.Scan(&label, &n); err != nil {
			ch <- prometheus.NewInvalidMetric(desc, err)
			return
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, n, label)
	}
}
