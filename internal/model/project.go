package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Project is the container knowledge and tasks can be scoped to (RFC-1250 §2).
//
//   - open: records are grouped but visible to everyone (RFC-1250 §11).
//   - closed: content is visible only to active members (§5), with a
//     narrow read-only reviewer tier for verification (§8).
//
// `listed` applies to closed projects only. false (the default) hides the
// project's very existence: a non-member gets the same not_found as for a
// project id that was never issued, so the endpoint cannot be used to
// enumerate what exists (§13). true advertises that the project exists
// without exposing anything inside it.
type Project struct {
	ProjectID        string `json:"project_id"`
	SchemaVersion    string `json:"schema_version"`
	Name             string `json:"name"`
	Objective        string `json:"objective"`
	Visibility       string `json:"visibility"`
	Listed           bool   `json:"listed"`
	MembershipPolicy string `json:"membership_policy"`
	Owner            string `json:"owner"`
	// PendingOwner is a nomination awaiting the nominee's acceptance.
	PendingOwner *string        `json:"pending_owner"`
	Status       string         `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	Metadata     map[string]any `json:"metadata"`
	// Repository is a normalized host/path identity, never a URL with a token.
	Repository string `json:"repository,omitempty"`
}

// Listing is the reduced view a non-member gets of a listed closed project:
// it exists, and that is all (RFC-1250 §5). Objective, membership and
// contents are withheld.
func (p Project) Listing() Project {
	return Project{
		ProjectID:        p.ProjectID,
		SchemaVersion:    p.SchemaVersion,
		Name:             p.Name,
		Visibility:       p.Visibility,
		Listed:           p.Listed,
		MembershipPolicy: p.MembershipPolicy,
		Status:           p.Status,
		Metadata:         map[string]any{},
	}
}

const (
	ProjectOpen     = "open"
	ProjectClosed   = "closed"
	ProjectActive   = "active"
	ProjectArchived = "archived"
)

type CreateProjectRequest struct {
	Name       string `json:"name"`
	Objective  string `json:"objective,omitempty"`
	Visibility string `json:"visibility,omitempty"` // defaults to open
	// Listed applies to closed projects: false (the default) hides the
	// project's existence from non-members entirely.
	Listed *bool `json:"listed,omitempty"`
	// MembershipPolicy decides whether outsiders may apply or must be
	// invited (RFC-1250 §2). Defaults to invite_only.
	MembershipPolicy string `json:"membership_policy,omitempty"`
	// Repository may be an HTTPS, SSH or git@host:path remote. It lets an
	// agent discover this project from the repository it is currently editing.
	Repository string `json:"repository,omitempty"`
}

func (r *CreateProjectRequest) Validate() *APIError {
	if r.Name == "" {
		return ValidationError("name is required")
	}
	if err := checkText("name", r.Name, MaxShortField); err != nil {
		return err
	}
	if err := checkText("objective", r.Objective, MaxTextField); err != nil {
		return err
	}
	switch r.Visibility {
	case "", ProjectOpen, ProjectClosed:
	default:
		return ValidationError("visibility must be open or closed")
	}
	switch r.MembershipPolicy {
	case "", PolicyInviteOnly, PolicyInviteOrApply:
	default:
		return ValidationError("membership_policy must be invite_only or invite_or_apply")
	}
	if r.Visibility != ProjectClosed && (r.Listed != nil || r.MembershipPolicy != "") {
		return ValidationError("listed and membership_policy apply only to closed projects")
	}
	if r.Repository != "" {
		if _, err := NormalizeRepository(r.Repository); err != nil {
			return ValidationError(err.Error())
		}
	}
	return nil
}

// NormalizeRepository removes transport and credentials so HTTPS, SSH and
// scp-style Git remotes for the same repository have one safe identity.
func NormalizeRepository(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("repository is required")
	}
	if at, colon := strings.Index(raw, "@"), strings.Index(raw, ":"); at > 0 && colon > at && !strings.Contains(raw, "://") {
		raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("repository must be a Git remote URL")
	}
	path := strings.Trim(strings.TrimSuffix(u.EscapedPath(), ".git"), "/")
	if path == "" || strings.Contains(path, "//") {
		return "", fmt.Errorf("repository must include an owner and name")
	}
	return strings.ToLower(u.Hostname()) + "/" + path, nil
}
