// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"fmt"

	"github.com/alibaba/open-code-review/internal/agent"
	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/config/toolsconfig"
	"github.com/spf13/cobra"
)

var delegatePromptsEffort string

var delegatePromptsCmd = &cobra.Command{
	Use:   "prompts [flags]",
	Short: "Output the system prompt `ocr review` gives each of its agents",
	Long: "Outputs, as JSON, the system message `ocr review` sends its grouping, plan, review and review-filter " +
		"agents, and how many review rounds the effort allows, for a host that runs each agent as its own session.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeDelegatePrompts(delegatePromptsEffort)
	},
}

func init() {
	delegatePromptsCmd.Flags().StringVar(&delegatePromptsEffort, "effort", string(template.EffortDefault),
		"review effort, as `ocr review --effort` takes it")
	delegateCmd.AddCommand(delegatePromptsCmd)
}

type delegatePromptsJSON struct {
	SchemaVersion string            `json:"schema_version"`
	Effort        string            `json:"effort"`
	ReviewRounds  int               `json:"review_rounds"`
	Prompts       map[string]string `json:"prompts"`
}

// executeDelegatePrompts prints each agent's system message, with effort and language applied as
// `ocr review` applies them.
func executeDelegatePrompts(effortName string) error {
	tpl, err := template.LoadDefault()
	if err != nil {
		return fmt.Errorf("load default template: %w", err)
	}
	effort, err := template.ParseEffort(effortName)
	if err != nil {
		return err
	}
	tpl.ApplyEffort(effort)
	entries, err := toolsconfig.Load("")
	if err != nil {
		return fmt.Errorf("load tools: %w", err)
	}
	cfgPath, err := defaultConfigPath()
	if err != nil {
		return err
	}
	appCfg, err := LoadAppConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load app config: %w", err)
	}
	lang := ""
	if appCfg != nil {
		lang = appCfg.Language
	}
	tpl.ApplyLanguage(lang)
	return writeDelegateJSON(delegatePromptsJSON{
		SchemaVersion: delegateSchemaVersion,
		Effort:        string(effort),
		ReviewRounds:  tpl.ReviewRounds(),
		Prompts:       agent.SystemPrompts(*tpl, agent.BuildToolDefs(entries, true)),
	})
}
