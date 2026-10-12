package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"aichatdeck/internal/model"
)

const availabilityTTL = 5 * time.Minute

func (s *AgentStore) UpdateAvailability(ctx context.Context, id string, req model.AvailabilityRequest) (model.AgentProfile, error) {
	if err := req.Validate(); err != nil {
		return model.AgentProfile{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_availability(agent_id,payload,updated_at) VALUES(?,?,?)
 ON CONFLICT(agent_id) DO UPDATE SET payload=excluded.payload,updated_at=excluded.updated_at`, id, toJSON(req), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return model.AgentProfile{}, err
	}
	return s.Profile(ctx, id)
}

func (s *AgentStore) Profile(ctx context.Context, id string) (model.AgentProfile, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return model.AgentProfile{}, err
	}
	out := model.AgentProfile{AgentID: a.AgentID, Capabilities: a.Capabilities, Tools: a.Tools}
	var payload, stamp string
	err = s.db.QueryRowContext(ctx, `SELECT payload,updated_at FROM agent_availability WHERE agent_id=?`, id).Scan(&payload, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	updated, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return out, err
	}
	expires := updated.Add(availabilityTTL)
	out.Availability = &model.Availability{AvailabilityRequest: fromJSON[model.AvailabilityRequest](payload), UpdatedAt: updated, ExpiresAt: expires, Stale: !time.Now().Before(expires), Source: "self_reported"}
	return out, nil
}

func (s *AgentStore) ListProfiles(ctx context.Context, cursor, capability, tool string, limit int) ([]model.AgentProfile, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	where := []string{"agent_id>?"}
	args := []any{cursor}
	if capability != "" {
		where = append(where, `EXISTS(SELECT 1 FROM jsonb_array_elements(agents.capabilities) c(value) WHERE c.value->>'name'=?)`)
		args = append(args, capability)
	}
	if tool != "" {
		where = append(where, `EXISTS(SELECT 1 FROM jsonb_array_elements(agents.tools) t(value) WHERE t.value->>'name'=?)`)
		args = append(args, tool)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM agents WHERE `+strings.Join(where, " AND ")+` ORDER BY agent_id LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, "", err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	out := []model.AgentProfile{}
	for _, id := range ids {
		p, err := s.Profile(ctx, id)
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	return out, next, nil
}
