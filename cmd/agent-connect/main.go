// Command agent-connect creates or resumes one agent identity and optionally
// requests access to a project. It is intended for an agent runtime that has
// a shell but has not started the MCP adapter yet.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/model"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "platform base URL")
	statePath := flag.String("state", "", "owner-only state file (required)")
	capsFlag := flag.String("caps", "", "comma-separated real capabilities (required)")
	toolsFlag := flag.String("tools", "", "comma-separated real local tool names")
	projectID := flag.String("project-id", "", "optional project ID to inspect/join")
	repository := flag.String("repository", "", "optional Git remote; auto-detected from origin when omitted")
	bindRepository := flag.Bool("bind-repository", false, "bind current repository to an explicitly supplied project you own")
	client := flag.String("client", "", "also register the MCP adapter with this client: claude (./.mcp.json) or codex (~/.codex/config.toml)")
	mcpCommand := flag.String("mcp-command", "", "absolute path of aichatdeck-mcp (default: next to this binary)")
	mcpName := flag.String("mcp-name", "aichatdeck", "MCP server name; use a distinct one per agent identity")
	flag.Parse()
	if *statePath == "" || *capsFlag == "" {
		log.Fatal("-state and -caps are required")
	}

	state, exists, err := agent.LoadState(*statePath)
	if err != nil {
		log.Fatalf("read state: %v", err)
	}
	c := agent.NewClient(*base)
	if exists {
		if state.BaseURL != "" && state.BaseURL != *base {
			log.Fatal("state belongs to another base URL; refuse to use or replace this identity")
		}
		c.AgentID, c.Token = state.AgentID, state.Token
	}
	req := model.RegisterRequest{Capabilities: namedCapabilities(*capsFlag), Tools: namedTools(*toolsFlag)}
	if err := c.Register(context.Background(), req); err != nil {
		// Register validates an existing credential before re-registering. In
		// particular, a 403 never falls through to a new identity here.
		log.Fatalf("connect agent: %v", err)
	}
	if !exists {
		if err := agent.SaveState(*statePath, agent.State{AgentID: c.AgentID, Token: c.Token, BaseURL: *base}); err != nil {
			log.Fatalf("save state: %v", err)
		}
	}
	fmt.Printf("agent_id=%s state=%s\n", c.AgentID, map[bool]string{true: "resumed", false: "created"}[exists])

	if *client != "" {
		cmd := *mcpCommand
		if cmd == "" {
			exe, err := os.Executable()
			if err != nil {
				log.Fatalf("locate mcp adapter: %v", err)
			}
			cmd = filepath.Join(filepath.Dir(exe), "aichatdeck-mcp")
		}
		abs, err := filepath.Abs(*statePath)
		if err != nil {
			log.Fatal(err)
		}
		msg, err := configureClient(*client, *mcpName, cmd, abs)
		if err != nil {
			log.Fatalf("configure client: %v", err)
		}
		fmt.Println(msg)
	}

	if *projectID == "" {
		repo := *repository
		if repo == "" {
			repo = originRemote()
		}
		if repo == "" {
			repo = agent.LocalRepository
		}
		found, created, err := c.EnsureProjectForRepository(context.Background(), repo)
		if err != nil {
			log.Fatalf("resolve project for repository: %v", err)
		}
		*projectID = found.ProjectID
		fmt.Printf("project_id=%s repository_match=true created=%t\n", *projectID, created)
	}
	p, err := c.GetProject(context.Background(), *projectID)
	if err != nil {
		log.Fatalf("project %q: %v", *projectID, err)
	}
	if *bindRepository {
		repo := *repository
		if repo == "" {
			repo = originRemote()
		}
		if repo == "" {
			log.Fatal("bind repository: no Git origin; pass -repository")
		}
		if _, err := c.BindProjectRepository(context.Background(), p.ProjectID, repo); err != nil {
			log.Fatalf("bind repository: %v", err)
		}
		fmt.Printf("project_id=%s repository_bound=true\n", p.ProjectID)
	}
	if p.Visibility == model.ProjectOpen {
		fmt.Printf("project_id=%s connection=ready visibility=open\n", p.ProjectID)
		return
	}
	if p.Owner != "" {
		fmt.Printf("project_id=%s connection=ready visibility=closed\n", p.ProjectID)
		return
	}
	if p.MembershipPolicy != model.PolicyInviteOrApply {
		fmt.Printf("project_id=%s connection=requires_invitation\n", p.ProjectID)
		return
	}
	m, err := c.ApplyToProject(context.Background(), *projectID)
	if err != nil {
		log.Fatalf("apply to project %q: %v", *projectID, err)
	}
	fmt.Printf("project_id=%s connection=application_pending membership=%s\n", p.ProjectID, m.Status)
}

func originRemote() string {
	out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func namedCapabilities(raw string) []model.Capability {
	var out []model.Capability
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, model.Capability{Name: name})
		}
	}
	return out
}

func namedTools(raw string) []model.Tool {
	var out []model.Tool
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, model.Tool{Name: name})
		}
	}
	return out
}
