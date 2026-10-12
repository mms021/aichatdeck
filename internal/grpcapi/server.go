// Package grpcapi serves aichatdeck.v1.TaskService. Like the REST handlers,
// it only translates: every rule lives in internal/app.
package grpcapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"aichatdeck/internal/app"
	pb "aichatdeck/internal/gen/aichatdeck/v1"
	"aichatdeck/internal/idgen"
	"aichatdeck/internal/model"
)

// EventsChannel is the bus channel the outbox relay pings after releasing
// events; WatchTasks wakes on it instead of waiting for its next poll.
const EventsChannel = "task-events"

type Server struct {
	pb.UnimplementedTaskServiceServer
	App *app.App
	// AllowAgent spends one unit of the agent's rate-limit budget; the REST
	// router supplies it so both transports share a single budget. Nil: no limit.
	AllowAgent func(agentID string) bool
	Logger     *log.Logger
}

// New returns a grpc.Server with TaskService behind bearer auth, plus server
// reflection (schema only, unauthenticated) so grpcurl works without .proto files.
func New(s *Server) *grpc.Server {
	g := grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryAuth),
		grpc.ChainStreamInterceptor(s.streamAuth),
	)
	pb.RegisterTaskServiceServer(g, s)
	reflection.Register(g)
	return g
}

type agentKey struct{}

func agentID(ctx context.Context) string { id, _ := ctx.Value(agentKey{}).(string); return id }

func (s *Server) authenticate(ctx context.Context) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) == 0 || !strings.HasPrefix(values[0], "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	id, err := s.App.Authenticate(ctx, strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer ")))
	if model.IsCode(err, model.ErrAccessDenied) {
		return nil, status.Error(codes.Unauthenticated, "invalid or revoked credential")
	}
	if err != nil {
		return nil, s.toStatus(ctx, err)
	}
	if s.AllowAgent != nil && !s.AllowAgent(id) {
		return nil, s.toStatus(ctx, model.RateLimited("rate limit exceeded, retry later"))
	}
	return context.WithValue(ctx, agentKey{}, id), nil
}

func (s *Server) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	ctx, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := next(ctx, req)
	if err != nil {
		return nil, s.toStatus(ctx, err)
	}
	return resp, nil
}

type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (a authedStream) Context() context.Context { return a.ctx }

func (s *Server) streamAuth(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	if strings.HasPrefix(info.FullMethod, "/grpc.reflection.") {
		return next(srv, ss)
	}
	ctx, err := s.authenticate(ss.Context())
	if err != nil {
		return err
	}
	if err := next(srv, authedStream{ss, ctx}); err != nil {
		return s.toStatus(ctx, err)
	}
	return nil
}

// toStatus maps RFC-1000 error codes onto gRPC codes and carries the
// original code in trailer metadata. Unknown errors become an opaque
// Internal with a trace ID, mirroring the REST API.
func (s *Server) toStatus(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	var apiErr *model.APIError
	if !errors.As(err, &apiErr) {
		traceID := idgen.New("trace")
		if s.Logger != nil {
			s.Logger.Printf("grpc internal error [%s]: %v", traceID, err)
		}
		apiErr = model.Internal()
		apiErr.TraceID = traceID
	}
	code := map[string]codes.Code{
		model.ErrNotFound:        codes.NotFound,
		model.ErrAccessDenied:    codes.PermissionDenied,
		model.ErrValidationError: codes.InvalidArgument,
		model.ErrConflict:        codes.FailedPrecondition,
		model.ErrRateLimited:     codes.ResourceExhausted,
		model.ErrUnavailable:     codes.Unavailable,
	}[apiErr.ErrorCode]
	if code == codes.OK {
		code = codes.Internal
	}
	_ = grpc.SetTrailer(ctx, metadata.Pairs("error-code", apiErr.ErrorCode))
	msg := apiErr.Message
	if apiErr.TraceID != "" {
		msg += " (trace_id " + apiErr.TraceID + ")"
	}
	return status.Error(code, msg)
}

func (s *Server) CreateTask(ctx context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
	in := model.CreateTaskRequest{
		Objective:            req.GetObjective(),
		Context:              req.GetContext().AsMap(),
		Constraints:          req.GetConstraints(),
		RequiredCapabilities: req.GetRequiredCapabilities(),
		RequiredTools:        req.GetRequiredTools(),
		SuccessCriteria:      req.GetSuccessCriteria(),
		ProjectID:            req.ProjectId,
		AllowedWorkRoles:     req.GetAllowedWorkRoles(),
	}
	if req.Deadline != nil {
		d := req.Deadline.AsTime()
		in.Deadline = &d
	}
	task, err := s.App.CreateTask(ctx, agentID(ctx), in, req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return &pb.CreateTaskResponse{Task: toTask(task)}, nil
}

func (s *Server) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	task, err := s.App.GetTask(ctx, agentID(ctx), req.GetTaskId())
	if err != nil {
		return nil, err
	}
	return &pb.GetTaskResponse{Task: toTask(task)}, nil
}

func (s *Server) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	filter := model.TaskFilter{
		Cursor:               req.GetCursor(),
		ProjectID:            req.ProjectId,
		RequiredCapabilities: req.GetRequiredCapabilities(),
		RequiredTools:        req.GetRequiredTools(),
	}
	if req.GetStatus() != pb.TaskStatus_TASK_STATUS_UNSPECIFIED {
		filter.Status = strings.TrimPrefix(req.GetStatus().String(), "TASK_STATUS_")
	}
	tasks, next, err := s.App.ListTasks(ctx, agentID(ctx), filter)
	if err != nil {
		return nil, err
	}
	out := &pb.ListTasksResponse{NextCursor: next}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, toTask(t))
	}
	return out, nil
}

func (s *Server) ClaimTask(ctx context.Context, req *pb.ClaimTaskRequest) (*pb.ClaimTaskResponse, error) {
	task, err := s.App.ClaimTask(ctx, agentID(ctx), req.GetTaskId())
	if err != nil {
		return nil, err
	}
	return &pb.ClaimTaskResponse{Task: toTask(task)}, nil
}

func (s *Server) HeartbeatTask(ctx context.Context, req *pb.HeartbeatTaskRequest) (*pb.HeartbeatTaskResponse, error) {
	task, err := s.App.HeartbeatTask(ctx, agentID(ctx), req.GetTaskId(), req.GetClaimId())
	if err != nil {
		return nil, err
	}
	return &pb.HeartbeatTaskResponse{Task: toTask(task)}, nil
}

// SubmitResult builds the same RESULT envelope a REST client posts to
// /messages, addressed to the task's creator.
func (s *Server) SubmitResult(ctx context.Context, req *pb.SubmitResultRequest) (*pb.SubmitResultResponse, error) {
	me := agentID(ctx)
	if req.GetStatus() == pb.ResultStatus_RESULT_STATUS_UNSPECIFIED {
		return nil, model.ValidationError("status is required")
	}
	task, err := s.App.GetTask(ctx, me, req.GetTaskId())
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(model.ResultPayload{
		ClaimID:   req.GetClaimId(),
		Summary:   req.GetSummary(),
		Status:    strings.ToLower(strings.TrimPrefix(req.GetStatus().String(), "RESULT_STATUS_")),
		Artifacts: req.GetArtifacts(),
		Metrics:   req.GetMetrics().AsMap(),
	})
	if err != nil {
		return nil, err
	}
	stored, created, err := s.App.PostMessage(ctx, me, model.Envelope{
		MessageID:       req.GetMessageId(),
		ProtocolVersion: "1.0",
		Type:            "RESULT",
		Sender:          me,
		Recipient:       task.CreatedBy,
		Timestamp:       time.Now().UTC(),
		TaskID:          task.TaskID,
		Priority:        "normal",
		Payload:         payload,
	})
	if err != nil {
		return nil, err
	}
	return &pb.SubmitResultResponse{MessageId: stored.MessageID, Created: created}, nil
}

func (s *Server) VerifyTask(ctx context.Context, req *pb.VerifyTaskRequest) (*pb.VerifyTaskResponse, error) {
	if req.GetVerdict() == pb.Verdict_VERDICT_UNSPECIFIED {
		return nil, model.ValidationError("verdict is required")
	}
	task, v, err := s.App.VerifyTask(ctx, agentID(ctx), req.GetTaskId(), model.VerifyRequest{
		AttemptID:       req.GetAttemptId(),
		TargetMessageID: req.GetTargetMessageId(),
		Verdict:         strings.ToLower(strings.TrimPrefix(req.GetVerdict().String(), "VERDICT_")),
		Rationale:       req.GetRationale(),
		Evidence:        req.GetEvidence(),
	})
	if err != nil {
		return nil, err
	}
	return &pb.VerifyTaskResponse{Task: toTask(task), Verification: &pb.Verification{
		VerificationId:  v.VerificationID,
		TaskId:          v.TaskID,
		VerifierId:      v.VerifierID,
		TargetMessageId: v.TargetMessageID,
		Verdict:         pb.Verdict(pb.Verdict_value["VERDICT_"+strings.ToUpper(v.Verdict)]),
		Rationale:       v.Rationale,
		Evidence:        v.Evidence,
		Timestamp:       timestamppb.New(v.Timestamp),
	}}, nil
}

func (s *Server) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	task, err := s.App.CancelTask(ctx, agentID(ctx), req.GetTaskId(), req.GetReason())
	if err != nil {
		return nil, err
	}
	return &pb.CancelTaskResponse{Task: toTask(task)}, nil
}

// watchPoll bounds how stale a watcher can be if a bus wakeup is lost.
const watchPoll = 2 * time.Second

func (s *Server) WatchTasks(req *pb.WatchTasksRequest, stream grpc.ServerStreamingServer[pb.WatchTasksResponse]) error {
	ctx := stream.Context()
	me := agentID(ctx)
	cursor := req.GetAfterSeq()
	if req.AfterSeq == nil {
		latest, err := s.App.Tasks.LatestEventSeq(ctx)
		if err != nil {
			return err
		}
		cursor = latest
	}
	wake, unsubscribe := s.App.Bus.Subscribe(ctx, EventsChannel)
	defer unsubscribe()
	for {
		events, err := s.App.Tasks.EventsAfter(ctx, me, cursor, req.ProjectId, 100)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := stream.Send(&pb.WatchTasksResponse{Event: toEvent(e)}); err != nil {
				return err
			}
			cursor = e.Seq
		}
		if len(events) == 100 {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(watchPoll):
		}
	}
}

func toTask(t model.Task) *pb.Task {
	return &pb.Task{
		TaskId:               t.TaskID,
		Objective:            t.Objective,
		Context:              toStruct(t.Context),
		Constraints:          t.Constraints,
		RequiredCapabilities: t.RequiredCapabilities,
		RequiredTools:        t.RequiredTools,
		SuccessCriteria:      t.SuccessCriteria,
		Status:               taskStatus(t.Status),
		Owner:                t.Owner,
		ProjectId:            t.ProjectID,
		CreatedBy:            t.CreatedBy,
		CreatedAt:            timestamppb.New(t.CreatedAt),
		Deadline:             toTimestamp(t.Deadline),
		ClaimedAt:            toTimestamp(t.ClaimedAt),
		LeaseExpiresAt:       toTimestamp(t.LeaseExpiresAt),
		ClaimId:              t.ClaimID,
		SubmittedMessageId:   t.SubmittedMessageID,
		AllowedWorkRoles:     t.AllowedWorkRoles,
	}
}

func toEvent(e model.TaskEvent) *pb.TaskEvent {
	var data map[string]any
	_ = json.Unmarshal(e.Data, &data)
	status, _ := data["status"].(string)
	return &pb.TaskEvent{
		Seq:        e.Seq,
		EventId:    e.EventID,
		Type:       e.Type,
		TaskId:     e.TaskID,
		OccurredAt: timestamppb.New(e.OccurredAt),
		Status:     taskStatus(status),
		Data:       toStruct(data),
	}
}

func taskStatus(s string) pb.TaskStatus {
	return pb.TaskStatus(pb.TaskStatus_value["TASK_STATUS_"+s])
}

func toTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// toStruct converts JSON-shaped data; anything that is not (it never is:
// it came from JSON) is dropped rather than failing the whole response.
func toStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}
