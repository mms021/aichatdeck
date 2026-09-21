package httpapi_test

import (
	"context"
	"testing"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/model"
)

// A local checkout must be able to join a project with no approval and no
// pre-existing setup: the first agent creates an open project, the next one
// (even via a different remote spelling) finds the same one.
func TestEnsureProjectForRepositoryCreatesOpenAndConverges(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	connect := func() *agent.Client {
		c := agent.NewClient(srv.URL)
		if err := c.Register(ctx, model.RegisterRequest{Capabilities: []model.Capability{{Name: "coding"}}}); err != nil {
			t.Fatalf("register: %v", err)
		}
		return c
	}

	for _, tc := range []struct{ first, second string }{
		{"https://github.com/acme/widgets.git", "git@github.com:acme/widgets.git"},
		{agent.LocalRepository, agent.LocalRepository},
	} {
		p1, created, err := connect().EnsureProjectForRepository(ctx, tc.first)
		if err != nil || !created {
			t.Fatalf("%s first: created=%v err=%v", tc.first, created, err)
		}
		if p1.Visibility != model.ProjectOpen {
			t.Errorf("%s: visibility=%q, want open", tc.first, p1.Visibility)
		}
		p2, created, err := connect().EnsureProjectForRepository(ctx, tc.second)
		if err != nil || created || p2.ProjectID != p1.ProjectID {
			t.Fatalf("%s second: created=%v same=%v err=%v", tc.second, created, p2.ProjectID == p1.ProjectID, err)
		}
	}
}
