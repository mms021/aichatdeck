// Command analytics is an example Kafka consumer of task events. It turns
// the event stream into Prometheus metrics the API cannot cheaply compute
// itself: task lead time (created -> verdict) and end-to-end event delay
// (state change committed -> event consumed).
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"

	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
)

func main() {
	brokers := getenv("KAFKA_BROKERS", "localhost:47092")
	topic := getenv("KAFKA_TOPIC", "aichatdeck.task-events")
	group := getenv("KAFKA_GROUP", "aichatdeck-analytics")
	metricsAddr := getenv("METRICS_ADDR", ":47093")

	reg := prometheus.NewRegistry()
	a := newAnalytics(reg)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	go func() {
		log.Fatal((&http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
	}()

	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Fatalf("kafka client: %v", err)
	}
	defer client.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("analytics consuming %s as %s (metrics on %s)", topic, group, metricsAddr)
	for {
		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil || fetches.IsClientClosed() {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			log.Printf("fetch %s[%d]: %v", topic, partition, err)
		})
		fetches.EachRecord(func(r *kgo.Record) {
			if err := a.handle(r.Value, time.Now()); err != nil {
				log.Printf("skip malformed record at %s[%d]@%d: %v", r.Topic, r.Partition, r.Offset, err)
			}
		})
		// Commit only after handling: at-least-once, duplicates filtered by event_id.
		if err := client.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			log.Printf("commit: %v", err)
		}
	}
}

type analytics struct {
	events     *prometheus.CounterVec
	duplicates prometheus.Counter
	leadTime   *prometheus.HistogramVec
	delay      prometheus.Histogram
	seen       *recent
}

func newAnalytics(reg prometheus.Registerer) *analytics {
	a := &analytics{
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aichatdeck_analytics_events_total", Help: "Task events consumed, duplicates excluded.",
		}, []string{"type"}),
		duplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "aichatdeck_analytics_duplicates_total", Help: "Redelivered events dropped by event_id.",
		}),
		leadTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "aichatdeck_analytics_task_lead_time_seconds", Help: "Task creation to final verdict.",
			Buckets: prometheus.ExponentialBuckets(1, 4, 9), // 1s .. ~18h
		}, []string{"verdict"}),
		delay: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "aichatdeck_analytics_event_delay_seconds", Help: "Commit of the state change to consumption of its event.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12), // 10ms .. ~20s
		}),
		seen: newRecent(10000),
	}
	reg.MustRegister(a.events, a.duplicates, a.leadTime, a.delay)
	return a
}

func (a *analytics) handle(value []byte, now time.Time) error {
	var e model.TaskEvent
	if err := json.Unmarshal(value, &e); err != nil {
		return err
	}
	if !a.seen.add(e.EventID) {
		a.duplicates.Inc()
		return nil
	}
	a.events.WithLabelValues(e.Type).Inc()
	a.delay.Observe(now.Sub(e.OccurredAt).Seconds())
	if e.Type != store.EventTaskVerified {
		return nil
	}
	var data struct {
		Verdict   string    `json:"verdict"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return err
	}
	if data.Verdict != "inconclusive" {
		a.leadTime.WithLabelValues(data.Verdict).Observe(e.OccurredAt.Sub(data.CreatedAt).Seconds())
	}
	return nil
}

// recent remembers the last n event IDs.
// ponytail: in-memory, so a restart may double-count events redelivered from
// the last uncommitted poll; persist IDs if exact counts ever matter.
type recent struct {
	ids  map[string]struct{}
	ring []string
	next int
}

func newRecent(n int) *recent {
	return &recent{ids: make(map[string]struct{}, n), ring: make([]string, n)}
}

// add reports whether id is new.
func (r *recent) add(id string) bool {
	if _, ok := r.ids[id]; ok {
		return false
	}
	if old := r.ring[r.next]; old != "" {
		delete(r.ids, old)
	}
	r.ring[r.next] = id
	r.ids[id] = struct{}{}
	r.next = (r.next + 1) % len(r.ring)
	return true
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
