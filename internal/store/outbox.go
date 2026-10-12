package store

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
