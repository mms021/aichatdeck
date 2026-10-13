// Package events releases task events from the PostgreSQL outbox: it gives
// each a seq (the order WatchTasks streams) and, when configured, publishes
// it to Kafka.
//
// Delivery is at-least-once: a crash after Kafka acknowledged a batch but
// before the rows were marked published resends that batch, so consumers
// deduplicate on event_id. One relay publishes at a time (an advisory lock),
// which keeps the outbox order — and with it per-task order — on the wire.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"aichatdeck/internal/db"
	"aichatdeck/internal/metrics"
	"aichatdeck/internal/model"
)

// Publisher sends one batch, returning only once every record is durable.
type Publisher interface {
	Publish(ctx context.Context, batch []model.TaskEvent) error
}

const relayLock = 7461627 // pg advisory lock key; schema application uses 7461626

type Relay struct {
	DB        *db.DB
	Publisher Publisher     // nil: assign seq only, no Kafka
	Notify    func()        // called after each released batch, e.g. to wake watchers
	BatchSize int           // default 100
	Interval  time.Duration // idle poll interval, default 500ms
	Logger    *log.Logger
}

// Run relays until ctx ends. Kafka being down only delays events: they wait
// in the outbox, and the API keeps working.
func (r *Relay) Run(ctx context.Context) {
	interval := r.Interval
	if interval == 0 {
		interval = 500 * time.Millisecond
	}
	lastCleanup := time.Now()
	for {
		n, err := r.Flush(ctx)
		if err != nil && ctx.Err() == nil {
			metrics.OutboxPublishErrors.Inc()
			r.logf("outbox relay: %v", err)
		}
		if time.Since(lastCleanup) > time.Hour {
			lastCleanup = time.Now()
			if _, err := r.DB.ExecContext(ctx, `DELETE FROM outbox WHERE published_at < now() - interval '1 day'`); err != nil {
				r.logf("outbox cleanup: %v", err)
			}
		}
		if n == r.batchSize() && err == nil {
			continue // backlog: keep draining
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Flush publishes the oldest unpublished events and marks them published,
// returning how many it sent. It sends nothing if another relay holds the lock.
func (r *Relay) Flush(ctx context.Context) (int, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var leader bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(?)`, relayLock).Scan(&leader); err != nil || !leader {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, event_id, event_type, aggregate_id, payload, created_at
		FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT ?`, r.batchSize())
	if err != nil {
		return 0, err
	}
	var ids []int64
	var batch []model.TaskEvent
	for rows.Next() {
		var id int64
		var e model.TaskEvent
		var data string
		if err := rows.Scan(&id, &e.EventID, &e.Type, &e.TaskID, &data, &e.OccurredAt); err != nil {
			rows.Close()
			return 0, err
		}
		e.Data = json.RawMessage(data)
		ids = append(ids, id)
		batch = append(batch, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(batch) == 0 {
		return 0, err
	}
	// Reserve a contiguous seq block. Only the lock holder writes seq, and a
	// failed round just leaves a gap.
	var base int64
	if err := tx.QueryRowContext(ctx, `SELECT nextval('outbox_seq')`).Scan(&base); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT setval('outbox_seq', ?)`, base+int64(len(batch))-1); err != nil {
		return 0, err
	}
	for i := range batch {
		batch[i].Seq = base + int64(i)
	}
	if r.Publisher != nil {
		if err := r.Publisher.Publish(ctx, batch); err != nil {
			return 0, fmt.Errorf("publish %d events: %w", len(batch), err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbox o SET published_at=now(), seq=?+u.ord-1
		FROM unnest(?::bigint[]) WITH ORDINALITY AS u(id, ord) WHERE o.id=u.id`, base, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if r.Publisher != nil {
		for _, e := range batch {
			metrics.EventsPublished.WithLabelValues(e.Type).Inc()
		}
	}
	if r.Notify != nil {
		r.Notify()
	}
	return len(batch), nil
}

func (r *Relay) batchSize() int {
	if r.BatchSize <= 0 {
		return 100
	}
	return r.BatchSize
}

func (r *Relay) logf(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
}

// Kafka publishes to one topic with an idempotent producer and acks from all
// in-sync replicas (franz-go defaults).
type Kafka struct{ client *kgo.Client }

func NewKafka(brokers []string, topic string) (*Kafka, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.AllowAutoTopicCreation(),
		kgo.RecordDeliveryTimeout(10*time.Second),
	)
	if err != nil {
		return nil, err
	}
	return &Kafka{client: client}, nil
}

func (k *Kafka) Publish(ctx context.Context, batch []model.TaskEvent) error {
	records := make([]*kgo.Record, 0, len(batch))
	for _, e := range batch {
		value, err := json.Marshal(e)
		if err != nil {
			return err
		}
		records = append(records, &kgo.Record{
			Key:     []byte(e.TaskID),
			Value:   value,
			Headers: []kgo.RecordHeader{{Key: "event_type", Value: []byte(e.Type)}},
		})
	}
	return k.client.ProduceSync(ctx, records...).FirstErr()
}

func (k *Kafka) Close() { k.client.Close() }
