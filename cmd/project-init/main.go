// Command project-init creates a platform project bound to the current Git
// repository and writes the non-secret project configuration agents consume.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/model"
	"aichatdeck/internal/projectconfig"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:47080", "platform base URL")
	statePath := flag.String("state", "", "existing owner state file (required)")
	name := flag.String("name", "", "project name (required)")
	objective := flag.String("objective", "", "project objective")
	visibility := flag.String("visibility", "open", "open or closed")
	repository := flag.String("repository", "", "Git remote; auto-detected from origin")
	flag.Parse()
	if *statePath == "" || *name == "" {
		log.Fatal("-state and -name are required")
	}
	state, ok, err := agent.LoadState(*statePath)
	if err != nil || !ok {
		log.Fatal("a valid existing owner state file is required")
	}
	if state.BaseURL != "" && state.BaseURL != *base {
		log.Fatal("state belongs to another base URL")
	}
	repo := *repository
	if repo == "" {
		repo = originRemote()
	}
	if repo == "" {
		log.Fatal("no Git origin; pass -repository")
	}
	c := agent.NewClient(*base)
	c.AgentID, c.Token = state.AgentID, state.Token
	p, err := c.CreateProject(context.Background(), model.CreateProjectRequest{Name: *name, Objective: *objective, Visibility: *visibility, Repository: repo})
	if err != nil {
		log.Fatalf("create project: %v", err)
	}
	path := filepath.Join(".aichatdeck", "project.json")
	if err := projectconfig.Save(path, projectconfig.Config{BaseURL: *base, ProjectID: p.ProjectID, Repository: p.Repository}); err != nil {
		log.Fatalf("write %s: %v", path, err)
	}
	fmt.Printf("project_id=%s config=%s\n", p.ProjectID, path)
}

func originRemote() string {
	out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
