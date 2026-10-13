package model

import (
	"encoding/json"
	"time"
)

// TaskEvent is one task lifecycle change from the outbox: the Kafka record
// value and the item streamed by gRPC WatchTasks. Seq is a gap-tolerant total
// order assigned when the event is released; Kafka record key is TaskID.
type TaskEvent struct {
	Seq        int64           `json:"seq"`
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	TaskID     string          `json:"task_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}
