package grpcapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"aichatdeck/internal/app"
	"aichatdeck/internal/bus"
	"aichatdeck/internal/db"
	"aichatdeck/internal/db/dbtest"
	"aichatdeck/internal/events"
	pb "aichatdeck/internal/gen/aichatdeck/v1"
	"aichatdeck/internal/model"
	"aichatdeck/internal/store"
)

type env struct {
	app    *app.App
	relay  *events.Relay
	client pb.TaskServiceClient
}

func setup(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(dbtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	fake := bus.NewFake()
	a := &app.App{
		Agents: store.NewAgentStore(d), Credentials: store.NewCredentialStore(d), Tasks: store.NewTaskStore(d),
		Messages: store.NewMessageStore(d), Artifacts: store.NewArtifactStore(d), Projects: store.NewProjectStore(d),
		Members: store.NewMembershipStore(d), Bus: fake,
	}
	lis := bufconn.Listen(1 << 20)
	srv := New(&Server{App: a})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	relay := &events.Relay{DB: d, Notify: func() { _ = fake.Publish(context.Background(), EventsChannel) }}
	return &env{app: a, relay: relay, client: pb.NewTaskServiceClient(conn)}
}

// agent registers an agent and returns its ID and an authenticated context.
func (e *env) agent(t *testing.T) (string, context.Context) {
	t.Helper()
	ctx := context.Background()
	a, err := e.app.Agents.Create(ctx, model.RegisterRequest{Capabilities: []model.Capability{{Name: "testing"}}})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := e.app.Credentials.Issue(ctx, a.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	return a.AgentID, metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+cred.Token)
}

func TestRequiresBearerToken(t *testing.T) {
	e := setup(t)
	_, err := e.client.ListTasks(context.Background(), &pb.ListTasksRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no token: %v", err)
	}
	bad := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer nope")
	if _, err := e.client.ListTasks(bad, &pb.ListTasksRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("bad token: %v", err)
	}
}

func TestTaskLifecycleOverGRPC(t *testing.T) {
	e := setup(t)
	_, creator := e.agent(t)
	_, worker := e.agent(t)
	_, other := e.agent(t)

	created, err := e.client.CreateTask(creator, &pb.CreateTaskRequest{Objective: "add two and two", RequiredCapabilities: []string{"testing"}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Task.TaskId
	if created.Task.Status != pb.TaskStatus_TASK_STATUS_OPEN {
		t.Fatalf("status = %v", created.Task.Status)
	}
	claimed, err := e.client.ClaimTask(worker, &pb.ClaimTaskRequest{TaskId: id})
	if err != nil {
		t.Fatal(err)
	}

	var trailer metadata.MD
	_, err = e.client.ClaimTask(other, &pb.ClaimTaskRequest{TaskId: id}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.FailedPrecondition || trailer.Get("error-code")[0] != model.ErrConflict {
		t.Fatalf("lost claim race: %v trailer=%v", err, trailer)
	}

	submit := &pb.SubmitResultRequest{TaskId: id, ClaimId: claimed.Task.ClaimId, MessageId: "grpc-result-1",
		Summary: "4", Status: pb.ResultStatus_RESULT_STATUS_SUCCESS}
	first, err := e.client.SubmitResult(worker, submit)
	if err != nil || !first.Created {
		t.Fatalf("submit: %+v %v", first, err)
	}
	retry, err := e.client.SubmitResult(worker, submit)
	if err != nil || retry.Created {
		t.Fatalf("retry must be a no-op: %+v %v", retry, err)
	}

	verified, err := e.client.VerifyTask(creator, &pb.VerifyTaskRequest{TaskId: id, TargetMessageId: "grpc-result-1", Verdict: pb.Verdict_VERDICT_VERIFIED})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Task.Status != pb.TaskStatus_TASK_STATUS_COMPLETED || verified.Verification.Verdict != pb.Verdict_VERDICT_VERIFIED {
		t.Fatalf("verify: %+v", verified)
	}
	if _, err := e.client.GetTask(other, &pb.GetTaskRequest{TaskId: "task-missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing task: %v", err)
	}
}

func TestWatchTasksStreamsOnlyVisibleEvents(t *testing.T) {
	e := setup(t)
	ownerID, owner := e.agent(t)
	_, outsider := e.agent(t)
	project, err := e.app.Projects.Create(context.Background(), ownerID, model.CreateProjectRequest{Name: "private", Visibility: model.ProjectClosed})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(owner, 10*time.Second)
	defer cancel()
	ownerStream, err := e.client.WatchTasks(ctx, &pb.WatchTasksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	outCtx, outCancel := context.WithTimeout(outsider, 10*time.Second)
	defer outCancel()
	outsiderStream, err := e.client.WatchTasks(outCtx, &pb.WatchTasksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let both watches pick their start seq

	if _, err := e.client.CreateTask(owner, &pb.CreateTaskRequest{Objective: "secret", ProjectId: &project.ProjectID}); err != nil {
		t.Fatal(err)
	}
	public, err := e.client.CreateTask(owner, &pb.CreateTaskRequest{Objective: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.relay.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	first, err := ownerStream.Recv()
	if err != nil || first.Event.Type != store.EventTaskCreated || first.Event.Status != pb.TaskStatus_TASK_STATUS_OPEN {
		t.Fatalf("owner first event: %+v %v", first, err)
	}
	second, err := ownerStream.Recv()
	if err != nil || second.Event.TaskId != public.Task.TaskId || second.Event.Seq <= first.Event.Seq {
		t.Fatalf("owner second event: %+v %v", second, err)
	}
	// The outsider sees the open-project event and nothing from the closed one.
	got, err := outsiderStream.Recv()
	if err != nil || got.Event.TaskId != public.Task.TaskId {
		t.Fatalf("outsider got %+v %v, want only the public task", got, err)
	}

	// Resuming after the first seq replays exactly what followed it.
	resumed, err := e.client.WatchTasks(ctx, &pb.WatchTasksRequest{AfterSeq: &first.Event.Seq})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := resumed.Recv(); err != nil || again.Event.EventId != second.Event.EventId {
		t.Fatalf("resume: %+v %v", again, err)
	}
}
