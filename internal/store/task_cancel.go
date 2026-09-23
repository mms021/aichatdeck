package store

import (
	"context"
	"strings"
	"time"

	"aichatdeck/internal/model"
)

// CancelOpen preserves the task and its history. The guarded update prevents
// cancellation racing with a worker's claim. Only the creator can cancel.
func (s *TaskStore) CancelOpen(ctx context.Context, actor, taskID, reason string) (model.Task, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 4096 {
		return model.Task{}, model.ValidationError("reason must contain 1 to 4096 bytes")
	}
	task, err := s.Get(ctx, actor, taskID)
	if err != nil {
		return model.Task{}, err
	}
	if task.CreatedBy != actor {
		return model.Task{}, model.AccessDenied("only the task creator may cancel it")
	}
	if task.Status == model.TaskCancelled {
		return task, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, context=json_set(context, '$.cancellation', json(?))
		WHERE task_id=? AND created_by=? AND status=? AND owner IS NULL`, model.TaskCancelled,
		toJSON(map[string]string{"reason": reason, "cancelled_by": actor, "cancelled_at": time.Now().UTC().Format(time.RFC3339)}),
		taskID, actor, model.TaskOpen)
	if err != nil {
		return model.Task{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return model.Task{}, model.Conflict("only unclaimed OPEN tasks can be cancelled")
	}
	return s.Get(ctx, actor, taskID)
}
