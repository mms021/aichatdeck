package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configureClient registers the MCP adapter with an agent client. An existing
// entry for name is left alone: silently repointing it could hand this
// client another agent's identity.
func configureClient(client, name, mcpCommand, statePath string) (string, error) {
	switch client {
	case "claude":
		return configureClaude(".mcp.json", name, mcpCommand, statePath)
	case "codex":
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			home = filepath.Join(h, ".codex")
		}
		return configureCodex(filepath.Join(home, "config.toml"), name, mcpCommand, statePath)
	}
	return "", fmt.Errorf("unknown -client %q (want claude or codex)", client)
}

func configureClaude(path, name, mcpCommand, statePath string) (string, error) {
	doc := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if _, ok := servers[name]; ok {
		return path + ": mcp server " + name + " already configured, left unchanged", nil
	}
	servers[name] = map[string]any{"command": mcpCommand, "args": []string{"-state", statePath}}
	doc["mcpServers"] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return "", err
	}
	return path + ": added mcp server " + name + " (restart the client to load it)", nil
}

func configureCodex(path, name, mcpCommand, statePath string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if strings.Contains(string(raw), "[mcp_servers."+name+"]") {
		return path + ": mcp server " + name + " already configured, left unchanged", nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	block := fmt.Sprintf("\n[mcp_servers.%s]\ncommand = %q\nargs = [\"-state\", %q]\n", name, mcpCommand, statePath)
	if _, err := f.WriteString(block); err != nil {
		return "", err
	}
	return path + ": added mcp server " + name + " (restart the client to load it)", nil
}
