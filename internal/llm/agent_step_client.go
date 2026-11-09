// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ExitAgentStepPending is the exit code of a run stopped at a call nobody has answered.
const ExitAgentStepPending = 75

// AgentStepClient lets an outside runner start one agent per model call. Each run
// replays the calls already answered on disk and stops at the first unanswered one.
type AgentStepClient struct {
	dir   string
	model string
	exit  func(int)

	mu    sync.Mutex
	calls int
}

// NewAgentStepClient builds a client that keeps its calls under cfg.AgentStepDir.
func NewAgentStepClient(cfg ClientConfig) *AgentStepClient {
	return &AgentStepClient{dir: cfg.AgentStepDir, model: cfg.Model, exit: os.Exit}
}

// CompletionsWithCtx returns the call's saved answer, or writes its job and stops the run.
func (c *AgentStepClient) CompletionsWithCtx(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.dir) == "" {
		return nil, fmt.Errorf("agent step: no directory configured; set %s", envOCRAgentStepDir)
	}
	c.calls++
	call, err := filepath.Abs(filepath.Join(c.dir, fmt.Sprintf("call-%03d", c.calls)))
	if err != nil {
		return nil, fmt.Errorf("agent step: call folder: %w", err)
	}
	resultPath := filepath.Join(call, "result.json")
	answer, err := readAgentCLIAnswer(resultPath)
	if err == nil {
		return agentCLIResponse(answer, req, c.model), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("agent step: %s: %w", filepath.Base(call), err)
	}
	promptPath := filepath.Join(call, "prompt.md")
	if _, statErr := os.Stat(promptPath); statErr == nil {
		return nil, fmt.Errorf("agent step: session wrote no answer for %s", filepath.Base(call))
	}
	if err := writeAgentStepJob(ctx, call, promptPath, renderAgentCLIPrompt(req, resultPath)); err != nil {
		return nil, err
	}
	// Plan and fact-check failures are logged and skipped, so an error here would not
	// stop the run. Exiting is the one stop every caller honors.
	c.exit(ExitAgentStepPending)
	return nil, errors.New("agent step: job written")
}

// writeAgentStepJob saves the call's prompt and what OCR says the call is for.
func writeAgentStepJob(ctx context.Context, call, promptPath, prompt string) error {
	if err := os.MkdirAll(call, 0o755); err != nil {
		return fmt.Errorf("agent step: call folder: %w", err)
	}
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return fmt.Errorf("agent step: write prompt: %w", err)
	}
	meta := map[string]string{}
	if m, ok := RequestMetaFromContext(ctx); ok {
		meta["task"], meta["group"] = m.TaskType, m.FilePath
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("agent step: encode meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(call, "meta.json"), raw, 0o644); err != nil {
		return fmt.Errorf("agent step: write meta: %w", err)
	}
	return nil
}
