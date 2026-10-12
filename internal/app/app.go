// Package app holds the task use-cases shared by every transport. REST
// (internal/httpapi) and gRPC (internal/grpcapi) only decode requests, call
// these methods with the authenticated agent ID, and encode the result — so
// project access, work-role and capability rules exist in exactly one place.
package app

import (
	"context"
	"encoding/json"

	"aichatdeck/internal/bus"
	"aichatdeck/internal/metrics"
	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
)

type App struct {
	Agents      *store.AgentStore
	Credentials *store.CredentialStore
	Tasks       *store.TaskStore
	Messages    *store.MessageStore
	Artifacts   *store.ArtifactStore
	Projects    *store.ProjectStore
	Members     *store.MembershipStore
	Bus         bus.Bus
}

// Authenticate resolves a bearer token to its agent.
func (a *App) Authenticate(ctx context.Context, token string) (string, error) {
	agentID, _, found, err := a.Credentials.LookupAgentByToken(ctx, token)
	if err != nil {
		return "", err
	}
	if !found {
		return "", model.AccessDenied("invalid or revoked credential")
	}
	return agentID, nil
}

// ProjectFor resolves a project for a caller, applying the RFC-1250 §13
// disclosure rules: full=false means a listed closed project the caller is
// outside of; an unlisted one reports not_found.
func (a *App) ProjectFor(ctx context.Context, projectID, agentID string) (model.Project, bool, error) {
	project, err := a.Projects.Get(ctx, projectID)
	if err != nil {
		return model.Project{}, false, err
	}
	if project.Visibility == model.ProjectOpen {
		return project, true, nil
	}
	canRead, err := a.Members.CanRead(ctx, projectID, agentID)
	if err != nil {
		return model.Project{}, false, err
	}
	if canRead || project.Owner == agentID {
		return project, true, nil
	}
	if project.Listed {
		return project, false, nil
	}
	return model.Project{}, false, model.NotFound("project not found")
}

// RequireProjectWriter is the state-changing counterpart to ProjectFor.
// Reviewer grants intentionally fail here: their narrow task visibility is
// for inspection, never for publishing, claiming or mutating evidence.
func (a *App) RequireProjectWriter(ctx context.Context, projectID, agentID string) error {
	project, err := a.Projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if project.Status != model.ProjectActive {
		return model.Conflict("cannot write to an archived project")
	}
	canWrite, err := a.Members.CanRead(ctx, projectID, agentID)
	if err != nil {
		return err
	}
	if !canWrite {
		return model.AccessDenied("project access is read-only")
	}
	return nil
}

func (a *App) CreateTask(ctx context.Context, agentID string, req model.CreateTaskRequest, idempotencyKey string) (model.Task, error) {
	if aerr := req.Validate(); aerr != nil {
		return model.Task{}, aerr
	}
	if req.ProjectID != nil {
		project, full, err := a.ProjectFor(ctx, *req.ProjectID, agentID)
		if err != nil {
			return model.Task{}, err
		}
		if !full {
			return model.Task{}, model.AccessDenied("only a project member may create tasks in it")
		}
		if project.Status != model.ProjectActive {
			return model.Task{}, model.Conflict("cannot create a task in an archived project")
		}
	}
	task, err := a.Tasks.Create(ctx, agentID, req, idempotencyKey)
	if err != nil {
		return model.Task{}, err
	}
	metrics.TaskEvents.WithLabelValues("created").Inc()
	return task, nil
}

func (a *App) GetTask(ctx context.Context, agentID, taskID string) (model.Task, error) {
	return a.Tasks.Get(ctx, agentID, taskID)
}

func (a *App) ListTasks(ctx context.Context, agentID string, filter model.TaskFilter) ([]model.Task, string, error) {
	tasks, next, err := a.Tasks.List(ctx, agentID, filter)
	if tasks == nil {
		tasks = []model.Task{}
	}
	return tasks, next, err
}

func (a *App) ClaimTask(ctx context.Context, agentID, taskID string) (model.Task, error) {
	// Listing with required_capabilities helps honest agents find suitable
	// work, but callers may omit that filter or call claim directly. Check the
	// registered profile again at the state-changing boundary.
	target, err := a.Tasks.Get(ctx, agentID, taskID)
	if err != nil {
		return model.Task{}, err
	}
	if target.ProjectID != nil {
		project, err := a.Projects.Get(ctx, *target.ProjectID)
		if err != nil {
			return model.Task{}, err
		}
		if project.Status != model.ProjectActive {
			return model.Task{}, model.Conflict("cannot claim a task in an archived project")
		}
		mayWork, err := a.Members.CanRead(ctx, project.ProjectID, agentID)
		if err != nil {
			return model.Task{}, err
		}
		if !mayWork {
			return model.Task{}, model.AccessDenied("reviewer access is read-only and cannot claim tasks")
		}
		member, memberErr := a.Members.Get(ctx, project.ProjectID, agentID)
		if memberErr != nil && !model.IsCode(memberErr, model.ErrNotFound) {
			return model.Task{}, memberErr
		}
		managed, err := a.Members.HasOrchestrator(ctx, project.ProjectID)
		if err != nil {
			return model.Task{}, err
		}
		restricted := managed || memberErr == nil && member.WorkRole != model.WorkRoleUnassigned
		if restricted && agentID != project.Owner && member.Role != model.RoleOrchestrator &&
			(memberErr != nil || member.Status != model.MemberActive || member.WorkRole == model.WorkRoleUnassigned || len(target.AllowedWorkRoles) == 0) {
			return model.Task{}, model.AccessDenied("await role assignment or choose a task explicitly allowed for your work role")
		}
		if len(target.AllowedWorkRoles) > 0 {
			member, err := a.Members.Get(ctx, project.ProjectID, agentID)
			if err != nil || member.Status != model.MemberActive || !workRoleAllowed(member.WorkRole, target.AllowedWorkRoles) {
				return model.Task{}, model.AccessDenied("agent lacks a project-assigned work role for this task")
			}
		}
	}
	claimer, err := a.Agents.Get(ctx, agentID)
	if err != nil {
		return model.Task{}, err
	}
	if !agentMatchesTask(claimer.Capabilities, target.RequiredCapabilities) {
		return model.Task{}, model.AccessDenied("agent lacks a required capability for this task")
	}
	if !agentMatchesTools(claimer.Tools, target.RequiredTools) {
		return model.Task{}, model.AccessDenied("agent lacks a required tool for this task")
	}
	task, err := a.Tasks.Claim(ctx, target.TaskID, agentID)
	if err != nil {
		return model.Task{}, err
	}
	metrics.TaskEvents.WithLabelValues("claimed").Inc()
	return task, nil
}

func (a *App) HeartbeatTask(ctx context.Context, agentID, taskID, claimID string) (model.Task, error) {
	if claimID == "" {
		return model.Task{}, model.ValidationError("claim_id is required")
	}
	target, err := a.Tasks.Get(ctx, agentID, taskID)
	if err != nil {
		return model.Task{}, err
	}
	if target.ProjectID != nil {
		if err := a.RequireProjectWriter(ctx, *target.ProjectID, agentID); err != nil {
			return model.Task{}, err
		}
	}
	return a.Tasks.Heartbeat(ctx, target.TaskID, agentID, claimID)
}

func (a *App) VerifyTask(ctx context.Context, agentID, taskID string, req model.VerifyRequest) (model.Task, model.Verification, error) {
	if aerr := req.Validate(); aerr != nil {
		return model.Task{}, model.Verification{}, aerr
	}
	// Verification records a state-changing judgement. A reviewer may inspect
	// its scoped task, but cannot alter it through a verdict.
	target, err := a.Tasks.Get(ctx, agentID, taskID)
	if err != nil {
		return model.Task{}, model.Verification{}, err
	}
	if target.ProjectID != nil {
		if err := a.RequireProjectWriter(ctx, *target.ProjectID, agentID); err != nil {
			return model.Task{}, model.Verification{}, err
		}
	}
	for _, artifactID := range req.Evidence {
		artifact, err := a.Artifacts.Get(ctx, agentID, artifactID)
		if err != nil || artifact.TaskID == nil || *artifact.TaskID != target.TaskID {
			return model.Task{}, model.Verification{}, model.ValidationError("verification evidence must be attached to this task")
		}
	}
	task, verification, err := a.Tasks.Verify(ctx, target.TaskID, agentID, req)
	if err != nil {
		return model.Task{}, model.Verification{}, err
	}
	metrics.Verdicts.WithLabelValues(verification.Verdict).Inc()
	return task, verification, nil
}

func (a *App) CancelTask(ctx context.Context, actor, taskID, reason string) (model.Task, error) {
	task, err := a.Tasks.Get(ctx, actor, taskID)
	if err != nil {
		return model.Task{}, err
	}
	if task.ProjectID != nil {
		if err := a.RequireProjectWriter(ctx, *task.ProjectID, actor); err != nil {
			return model.Task{}, err
		}
	}
	task, err = a.Tasks.CancelOpen(ctx, actor, task.TaskID, reason)
	if err != nil {
		return model.Task{}, err
	}
	metrics.TaskEvents.WithLabelValues("cancelled").Inc()
	return task, nil
}

// PostMessage validates and persists an envelope (RFC-1100 §5). isNew is
// false for a replay of an already-stored message_id.
func (a *App) PostMessage(ctx context.Context, agentID string, env model.Envelope) (model.Envelope, bool, error) {
	if aerr := env.Validate(); aerr != nil {
		return model.Envelope{}, false, aerr
	}
	// Sender spoofing is rejected: the sender MUST equal the authenticated caller.
	if env.Sender != agentID {
		return model.Envelope{}, false, model.AccessDenied("sender must equal the authenticated agent")
	}
	if env.Recipient != model.BroadcastRecipient {
		if _, err := a.Agents.Get(ctx, env.Recipient); err != nil {
			return model.Envelope{}, false, err
		}
	}
	if env.TaskID != "" {
		if err := a.checkTaskMessage(ctx, agentID, env); err != nil {
			return model.Envelope{}, false, err
		}
	}
	stored, isNew, err := a.Messages.Insert(ctx, env)
	if err != nil {
		return model.Envelope{}, false, err
	}
	if isNew {
		metrics.Messages.WithLabelValues(stored.Type).Inc()
		if stored.Type == "RESULT" && stored.TaskID != "" {
			metrics.TaskEvents.WithLabelValues("submitted").Inc()
		}
		channel := bus.BroadcastChannel
		if stored.Recipient != model.BroadcastRecipient {
			channel = bus.AgentChannel(stored.Recipient)
		}
		_ = a.Bus.Publish(ctx, channel) // best-effort wakeup; PostgreSQL is the source of truth either way
	}
	return stored, isNew, nil
}

// checkTaskMessage: a task id makes the message part of that task's record.
// Resolve it through the sender's scope first, then require write-capable
// project membership for every participant. Otherwise a member could relay
// a closed task's payload to an outsider through a direct message.
func (a *App) checkTaskMessage(ctx context.Context, agentID string, env model.Envelope) error {
	task, err := a.Tasks.Get(ctx, agentID, env.TaskID)
	if err != nil {
		return err
	}
	if task.ProjectID != nil {
		if err := a.RequireProjectWriter(ctx, *task.ProjectID, agentID); err != nil {
			return err
		}
		if env.Recipient == model.BroadcastRecipient {
			project, err := a.Projects.Get(ctx, *task.ProjectID)
			if err != nil {
				return err
			}
			if project.Visibility == model.ProjectClosed {
				return model.AccessDenied("a closed-project task cannot broadcast messages")
			}
		} else {
			if _, err := a.Tasks.Get(ctx, env.Recipient, task.TaskID); err != nil {
				return model.AccessDenied("recipient may not access this task")
			}
			if err := a.RequireProjectWriter(ctx, *task.ProjectID, env.Recipient); err != nil {
				return model.AccessDenied("recipient may not receive project task messages")
			}
		}
	}
	if env.Type != "RESULT" {
		return nil
	}
	if task.Owner == nil || *task.Owner != agentID {
		return model.AccessDenied("only the task's current owner may report a RESULT")
	}
	if env.Recipient != task.CreatedBy {
		return model.ValidationError("a task RESULT must be sent to its creator")
	}
	var result model.ResultPayload
	if err := json.Unmarshal(env.Payload, &result); err != nil {
		return model.ValidationError("invalid RESULT payload")
	}
	for _, artifactID := range result.Artifacts {
		artifact, err := a.Artifacts.Get(ctx, agentID, artifactID)
		if err != nil || artifact.TaskID == nil || *artifact.TaskID != task.TaskID || artifact.CreatedBy != agentID {
			return model.ValidationError("RESULT artifacts must be created by the task owner and attached to this task")
		}
	}
	return nil
}

func workRoleAllowed(have string, allowed []string) bool {
	for _, role := range allowed {
		if have == role {
			return true
		}
	}
	return false
}

func agentMatchesTools(tools []model.Tool, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := make([]string, 0, len(tools))
	for _, tool := range tools {
		have = append(have, tool.Name)
	}
	return hasAnyName(have, required)
}

// agentMatchesTask accepts unqualified tasks for every agent. A qualified
// task needs at least one relevant declared capability, matching the list
// endpoint and RFC-1500's capability_fit model.
func agentMatchesTask(capabilities []model.Capability, required []string) bool {
	names := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		names = append(names, capability.Name)
	}
	return len(required) == 0 || hasAnyName(names, required)
}

func hasAnyName(have, required []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, name := range have {
		set[name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := set[name]; ok {
			return true
		}
	}
	return false
}
