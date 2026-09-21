package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureClientsAddOnceAndKeepExisting(t *testing.T) {
	dir := t.TempDir()

	claude := filepath.Join(dir, ".mcp.json")
	os.WriteFile(claude, []byte(`{"mcpServers":{"other":{"command":"x"}},"keep":1}`), 0o644)
	for i, want := range []string{"added", "already configured"} {
		msg, err := configureClaude(claude, "aichatdeck", "/bin/mcp", "/s.json")
		if err != nil || !strings.Contains(msg, want) {
			t.Fatalf("claude run %d: %q %v", i, msg, err)
		}
	}
	got, _ := os.ReadFile(claude)
	for _, s := range []string{`"other"`, `"keep"`, `"aichatdeck"`, `"/s.json"`} {
		if !strings.Contains(string(got), s) {
			t.Errorf(".mcp.json lost %s: %s", s, got)
		}
	}

	codex := filepath.Join(dir, "config.toml")
	os.WriteFile(codex, []byte("model = \"x\"\n"), 0o600)
	for i, want := range []string{"added", "already configured"} {
		msg, err := configureCodex(codex, "aichatdeck", "/bin/mcp", "/s.json")
		if err != nil || !strings.Contains(msg, want) {
			t.Fatalf("codex run %d: %q %v", i, msg, err)
		}
	}
	got, _ = os.ReadFile(codex)
	if strings.Count(string(got), "[mcp_servers.aichatdeck]") != 1 || !strings.HasPrefix(string(got), "model = ") {
		t.Errorf("config.toml wrong: %s", got)
	}
}
