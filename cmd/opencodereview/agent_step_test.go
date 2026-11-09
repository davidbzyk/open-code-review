// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
)

func TestAgentStepRunsOneCallAtATime(t *testing.T) {
	step := llm.NewAgentStepClient(llm.ClientConfig{AgentStepDir: "steps"})
	if got := reviewConcurrency(step, 8); got != 1 {
		t.Fatalf("agent-step concurrency %d, want 1", got)
	}
	cli := llm.NewAgentCLIClient(llm.ClientConfig{AgentCommand: "true"})
	if got := reviewConcurrency(cli, 8); got != 8 {
		t.Fatalf("agent-cli concurrency %d, want the flag's 8", got)
	}
}
