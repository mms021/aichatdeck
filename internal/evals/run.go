package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aichatdeck/internal/agent"
	"aichatdeck/internal/model"
)

// OracleJudge answers each case's first acceptable verdict. It must score
// 100%: anything less is a bug in the harness or the grader, not the model.
type OracleJudge struct{ Cases []VerifierCase }

func (OracleJudge) Name() string { return "oracle" }

func (j OracleJudge) Verify(_ context.Context, task model.Task, _ agent.VerificationInput) (string, string, *agent.Call, error) {
	for _, c := range j.Cases {
		if task.TaskID == "task-eval-"+c.ID {
			return c.Acceptable[0], "oracle", nil, nil
		}
	}
	return "", "", nil, fmt.Errorf("oracle: unknown task %s", task.TaskID)
}

// NewRunID is sortable and readable: 20261013-142501-claude-opus-5.
func NewRunID(label string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, strings.ToLower(label))
	return time.Now().UTC().Format("20060102-150405") + "-" + safe
}

// Run is one immutable output directory.
type Run struct{ Dir string }

// CreateRun refuses to reuse a directory: an eval result, once written, is
// never overwritten by a later run.
func CreateRun(root, runID string) (Run, error) {
	dir := filepath.Join(root, runID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Run{}, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Run{}, fmt.Errorf("run %s already exists; runs are never overwritten", dir)
		}
		return Run{}, err
	}
	return Run{Dir: dir}, nil
}

func (r Run) WriteJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.Dir, name), append(b, '\n'), 0o644)
}

func WriteJSONL[T any](r Run, name string, rows []T) error {
	f, err := os.Create(filepath.Join(r.Dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return f.Close()
}

func (r Run) WriteText(name, text string) error {
	return os.WriteFile(filepath.Join(r.Dir, name), []byte(text), 0o644)
}
