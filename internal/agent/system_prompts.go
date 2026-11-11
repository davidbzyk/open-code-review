// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"strings"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/session"
)

// SystemPrompts returns the system message `ocr review` sends each of its agents, keyed by task
// type, so a host that runs every agent as its own session can give it the same role.
func SystemPrompts(tpl template.Template, planTools []llm.ToolDef) map[string]string {
	prompts := map[string]string{}
	add := func(task session.TaskType, conv *template.LlmConversation) {
		if conv == nil {
			return
		}
		for _, m := range conv.Messages {
			if m.Role == "system" {
				prompts[string(task)] = strings.ReplaceAll(m.Content, "{{plan_tools}}", formatToolDefs(planTools))
				return
			}
		}
	}
	add(session.GroupingTask, tpl.GroupingTask)
	add(session.PlanTask, tpl.PlanTask)
	add(session.MainTask, &tpl.MainTask)
	add(session.ReviewFilterTask, tpl.ReviewFilterTask)
	return prompts
}
