package store

import (
	"aichatdeck/internal/model"
	"context"
	"database/sql"
	"time"
)

type Leadership struct {
	AgentID    string    `json:"agent_id"`
	Generation int64     `json:"generation"`
	LeaseUntil time.Time `json:"lease_until,omitzero"`
}

func (s *ProjectStore) Leadership(ctx context.Context, id string) (Leadership, error) {
	var l Leadership
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,generation,lease_until FROM project_leadership WHERE project_id=?`, id).Scan(&l.AgentID, &l.Generation, &l.LeaseUntil)
	if err == sql.ErrNoRows {
		return l, nil
	}
	return l, err
}

func (s *MembershipStore) AssignWithLease(ctx context.Context, project, actor, subject, role string, generation int64) (model.ProjectMembership, error) {
	now := dbNow()
	res, err := s.db.ExecContext(ctx, `INSERT INTO project_members(project_id,agent_id,status,role,work_role,created_at,joined_at)
 SELECT project_id,?,'active','member',?,?,? FROM project_leadership WHERE project_id=? AND agent_id=? AND generation=? AND lease_until>?
 ON CONFLICT(project_id,agent_id) DO UPDATE SET work_role=excluded.work_role`, subject, role, now, now, project, actor, generation, now)
	if err != nil {
		return model.ProjectMembership{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.ProjectMembership{}, model.Conflict("stale leadership lease")
	}
	return s.Get(ctx, project, subject)
}

// expected=0 acquires an absent/expired lease; otherwise renew or hand off.
// Each acquisition/handoff increments generation; renewal retains it.
func (s *ProjectStore) ChangeLeadership(ctx context.Context, id, actor, target string, expected int64) (Leadership, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Leadership{}, err
	}
	defer tx.Rollback()
	now := dbNow()
	until := dbNow().Add(5 * time.Minute)
	// Lock the project row: with no leadership row yet there is nothing else
	// to lock, and two first acquisitions would both pass the check below.
	var locked string
	if err = tx.QueryRowContext(ctx, `SELECT project_id FROM projects WHERE project_id=? FOR UPDATE`, id).Scan(&locked); err != nil {
		if err == sql.ErrNoRows {
			return Leadership{}, model.NotFound("project not found")
		}
		return Leadership{}, err
	}
	var l Leadership
	err = tx.QueryRowContext(ctx, `SELECT agent_id,generation,lease_until FROM project_leadership WHERE project_id=?`, id).Scan(&l.AgentID, &l.Generation, &l.LeaseUntil)
	if err != nil && err != sql.ErrNoRows {
		return l, err
	}
	if expected == 0 {
		if err == nil && l.LeaseUntil.After(now) {
			return l, model.Conflict("leadership lease is active")
		}
		target = actor
	} else if err != nil || l.AgentID != actor || l.Generation != expected || !l.LeaseUntil.After(now) {
		return l, model.Conflict("stale leadership generation or expired lease")
	}
	generation := l.Generation
	if expected == 0 || target != actor {
		generation++
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_leadership VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET agent_id=excluded.agent_id,generation=excluded.generation,lease_until=excluded.lease_until`, id, target, generation, until)
	if err != nil {
		return l, err
	}
	if err = tx.Commit(); err != nil {
		return l, err
	}
	return Leadership{target, generation, until}, nil
}

func (s *ProjectStore) SaveProgress(ctx context.Context, id, actor, task, claim string, generation int64, payload any) error {
	now := dbNow()
	var result sql.Result
	var err error
	if task != "" {
		result, err = s.db.ExecContext(ctx, `INSERT INTO progress_checkpoints(project_id,task_id,author,payload,created_at)
   SELECT project_id,task_id,?, ?,? FROM tasks WHERE task_id=? AND project_id=? AND owner=? AND status='CLAIMED' AND claim_id=? AND lease_expires_at>?`,
			actor, toJSON(payload), now, task, id, actor, claim, now)
	} else {
		result, err = s.db.ExecContext(ctx, `INSERT INTO progress_checkpoints(project_id,author,generation,payload,created_at)
   SELECT project_id,?,?,?,? FROM project_leadership WHERE project_id=? AND agent_id=? AND generation=? AND lease_until>?`,
			actor, generation, toJSON(payload), now, id, actor, generation, now)
	}
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return model.Conflict("stale task claim or leadership lease")
	}
	return nil
}

func (s *ProjectStore) Progress(ctx context.Context, id string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,COALESCE(task_id,''),author,payload,created_at FROM progress_checkpoints WHERE project_id=? ORDER BY seq DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var seq int64
		var task, author, payload, stamp string
		if err := rows.Scan(&seq, &task, &author, &payload, &stamp); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"seq": seq, "task_id": task, "author": author, "progress": fromJSON[map[string]any](payload), "created_at": stamp})
	}
	return out, rows.Err()
}
