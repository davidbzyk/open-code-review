// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/config/toolsconfig"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

// systemCaptureClient keeps the system message of every request, keyed by its task type.
type systemCaptureClient struct{ sent map[string]string }

func (c *systemCaptureClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	task := string(session.GroupingTask)
	if meta, ok := llm.RequestMetaFromContext(ctx); ok {
		task = meta.TaskType
	}
	c.sent[task] = req.Messages[0].ExtractText()
	content := ""
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Content: &content}}}}, nil
}

func TestSystemPromptsAreWhatEachAgentIsSent(t *testing.T) {
	setTestHome(t, t.TempDir())
	tpl, err := template.LoadDefault()
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	entries, err := toolsconfig.Load("")
	if err != nil {
		t.Fatalf("load tools: %v", err)
	}
	planTools := BuildToolDefs(entries, true)
	client := &systemCaptureClient{sent: map[string]string{}}
	a := New(Args{Template: *tpl, LLMClient: client, Model: "fake", PlanToolDefs: planTools, Tools: tool.NewRegistry()})
	t.Cleanup(func() { _ = a.Session().Finalize() })
	d := model.Diff{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b\n", Insertions: 1, Deletions: 1}
	g := FileGroup{Label: "a.go", Diffs: []model.Diff{d}}
	ctx := context.Background()

	if _, err := a.executeGroupPlanPhase(ctx, g, "diffs", "", "rule"); err != nil {
		t.Fatalf("plan: %v", err)
	}
	a.args.CommentCollector.Add(model.LlmComment{Path: "a.go", Content: "x"})
	a.executeGroupReviewFilter(ctx, g, nil)
	if _, _, err := callGroupingLLM(ctx, []model.Diff{d}, client, "fake", tpl.GroupingTask, 100, nil); err == nil {
		t.Fatal("an empty grouping answer parsed")
	}
	client.sent[string(session.MainTask)] = a.buildMainTaskMessages("rule", "", "diffs", "", "")[0].ExtractText()

	prompts := SystemPrompts(*tpl, planTools)
	if len(prompts) != 4 || len(client.sent) != 4 {
		t.Fatalf("prompts for %d tasks and %d sent, want grouping, plan, main and filter", len(prompts), len(client.sent))
	}
	for task, want := range client.sent {
		if prompts[task] != want {
			t.Errorf("%s: SystemPrompts differs from the system message OCR sends", task)
		}
	}
}
