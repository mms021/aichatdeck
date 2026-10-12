package store

import (
	"context"
	"encoding/json"

	"aichatdeck/internal/model"
)

// Task event types written to the outbox (see schema.sql).
const (
	EventTaskCreated   = "task.created"
	EventTaskClaimed   = "task.claimed"
	EventTaskRequeued  = "task.requeued"
	EventTaskSubmitted = "task.submitted"
	EventTaskVerified  = "task.verified"
	EventTaskCancelled = "task.cancelled"
)

// taskSnapshot is the payload every task event carries: the task's state
// after the change, enough for a consumer to act without calling the API.
const taskSnapshot = `jsonb_build_object('status', status, 'project_id', project_id,
	'created_by', created_by, 'owner', owner, 'created_at', created_at)`

// withTaskEvent wraps an INSERT or UPDATE on tasks so the same statement
// appends one outbox row per affected task. RowsAffected still equals the
// number of tasks changed, so compare-and-swap checks keep working.
func withTaskEvent(event, change string) string {
	return `WITH changed AS (` + change + ` RETURNING *)
		INSERT INTO outbox (event_type, aggregate_id, payload)
		SELECT '` + event + `', task_id, ` + taskSnapshot + ` FROM changed`
}

// EventsAfter returns released task events with seq > afterSeq for tasks the
// viewer can see, optionally narrowed to one project, oldest first.
func (s *TaskStore) EventsAfter(ctx context.Context, viewer string, afterSeq int64, projectID *string, limit int) ([]model.TaskEvent, error) {
	args := append([]any{afterSeq}, visibleScopeArgs(viewer, true)...)
	q := `SELECT o.seq, o.event_id, o.event_type, o.aggregate_id, o.created_at, o.payload
		FROM outbox o JOIN tasks ON tasks.task_id = o.aggregate_id
		WHERE o.seq > ? AND ` + visibleScopeSQL("tasks.task_id")
	if projectID != nil {
		q += ` AND tasks.project_id = ?`
		args = append(args, *projectID)
	}
	q += ` ORDER BY o.seq LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TaskEvent
	for rows.Next() {
		var e model.TaskEvent
		var data string
		if err := rows.Scan(&e.Seq, &e.EventID, &e.Type, &e.TaskID, &e.OccurredAt, &data); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestEventSeq is where a live-only watch starts.
func (s *TaskStore) LatestEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM outbox`).Scan(&seq)
	return seq, err
}
