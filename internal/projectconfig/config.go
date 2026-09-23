// Package projectconfig stores the non-secret platform binding committed with
// a repository. Agent credentials deliberately never belong in this file.
package projectconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const RelativePath = ".aichatdeck/project.json"

type Config struct {
	SchemaVersion string `json:"schema_version"`
	BaseURL       string `json:"base_url"`
	ProjectID     string `json:"project_id"`
	Repository    string `json:"repository"`
}

func Load(path string) (Config, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, false, fmt.Errorf("parse project config: %w", err)
	}
	if c.SchemaVersion != "1.0" || c.BaseURL == "" || c.ProjectID == "" || c.Repository == "" {
		return Config{}, false, fmt.Errorf("invalid project config")
	}
	return c, true, nil
}

func Save(path string, c Config) error {
	if c.SchemaVersion == "" {
		c.SchemaVersion = "1.0"
	}
	if c.SchemaVersion != "1.0" || c.BaseURL == "" || c.ProjectID == "" || c.Repository == "" {
		return fmt.Errorf("invalid project config")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
