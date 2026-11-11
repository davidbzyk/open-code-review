// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alibaba/open-code-review/internal/agent"
	"github.com/alibaba/open-code-review/internal/delegate"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/spf13/cobra"
)

// delegateGroupOptions adds the output folder and the answer file to the shared flags.
type delegateGroupOptions struct {
	delegateOptions
	outDir string
	apply  string
}

var delegateGroupOpts delegateGroupOptions
var delegateMergeOpts delegateGroupOptions

var delegateGroupCmd = &cobra.Command{
	Use:   "group [flags] --out <dir>",
	Short: "Partition reviewable files into review groups for sub-agents",
	Long: `Partitions the reviewable files the way "ocr review" does, without calling a model.

Without --apply it writes groups.json when OCR groups the change set itself, or
grouping-request.md (the request "ocr review" sends its grouping model) for a host
agent to answer. With --apply <answer file> it turns that answer into groups.json
and groups/<NN>.json, one slice per reviewer; an answer that is missing or does not
parse gives every file its own group, as a failed grouping call does in a review.`,
	Example: `  # Write the grouping request, or the groups when no model is needed
  ocr delegate group --from main --to feature --out .ocr-review

  # Turn the host agent's answer into groups
  ocr delegate group --from main --to feature --out .ocr-review --apply .ocr-review/grouping.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateDelegateOptions(&delegateGroupOpts.delegateOptions); err != nil {
			return err
		}
		return executeDelegateGroup(delegateGroupOpts, cmd.Flags().Changed("apply"))
	},
}

var delegateMergeCmd = &cobra.Command{
	Use:   "merge --out <dir>",
	Short: "Merge every group's comments into merged.json",
	Long: `Reads groups.json and each group's comments.<NN>.json under --out and writes
merged.json: every comment tagged with its group, and the groups whose reviewer
left no usable file listed as unfinished.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if delegateMergeOpts.format != "text" && delegateMergeOpts.format != "json" {
			return fmt.Errorf("invalid --format value %q: must be 'text' or 'json'", delegateMergeOpts.format)
		}
		return executeDelegateMerge(delegateMergeOpts)
	},
}

func init() {
	registerDelegateFlags(delegateGroupCmd, &delegateGroupOpts.delegateOptions)
	delegateGroupCmd.Flags().StringVar(&delegateGroupOpts.outDir, "out", "", "folder the grouping files are written to (required)")
	delegateGroupCmd.Flags().StringVar(&delegateGroupOpts.apply, "apply", "", "file holding the host agent's answer to the grouping request")
	_ = delegateGroupCmd.MarkFlagRequired("out")

	delegateMergeCmd.Flags().StringVar(&delegateMergeOpts.outDir, "out", "", "folder holding groups.json and the comments files (required)")
	delegateMergeCmd.Flags().StringVarP(&delegateMergeOpts.format, "format", "f", "text", "output format: text or json")
	_ = delegateMergeCmd.MarkFlagRequired("out")

	delegateCmd.AddCommand(delegateGroupCmd)
	delegateCmd.AddCommand(delegateMergeCmd)
}

// delegateGroupJSON is what `ocr delegate group --format json` prints.
type delegateGroupJSON struct {
	SchemaVersion string `json:"schema_version"`
	NeedsAnswer   bool   `json:"needs_answer"`
	Strategy      string `json:"strategy"`
	Files         int    `json:"files"`
	Groups        int    `json:"groups"`
	Fallback      string `json:"fallback,omitempty"`
	Request       string `json:"request,omitempty"`
	GroupsFile    string `json:"groups_file,omitempty"`
}

func executeDelegateGroup(opts delegateGroupOptions, applying bool) error {
	dc, err := loadDelegateContext(opts.delegateOptions)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.outDir, 0o755); err != nil {
		return fmt.Errorf("create --out folder: %w", err)
	}

	ctx := context.Background()
	args := agent.Args{
		RepoDir:    dc.cc.RepoDir,
		From:       dc.opts.from,
		To:         dc.opts.to,
		Commit:     dc.opts.commit,
		FileFilter: dc.cc.FileFilter,
		GitRunner:  dc.cc.GitRunner,
	}
	var grouping *agent.DelegateGrouping
	if applying {
		answer, readErr := os.ReadFile(opts.apply)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("read --apply file: %w", readErr)
		}
		// A missing answer is an empty one: the grouping agent wrote nothing.
		grouping, err = agent.ApplyDelegateGrouping(ctx, args, *dc.cc.Template, string(answer))
	} else {
		grouping, err = agent.PlanDelegateGrouping(ctx, args, *dc.cc.Template)
	}
	if err != nil {
		return fmt.Errorf("grouping failed: %w", err)
	}

	status := delegateGroupJSON{SchemaVersion: delegateSchemaVersion, Strategy: grouping.Strategy}
	for _, f := range grouping.Changed {
		if f.Status != "deleted" {
			status.Files++
		}
	}
	if grouping.Groups == nil {
		status.NeedsAnswer = true
		status.Request = filepath.Join(opts.outDir, delegate.GroupingRequestFile)
		if err := os.WriteFile(status.Request, []byte(groupingRequestMarkdown(grouping.Request)), 0o644); err != nil {
			return fmt.Errorf("write grouping request: %w", err)
		}
	} else {
		groups := delegate.Groups{
			Mode:         dc.reviewMode(),
			From:         dc.opts.from,
			To:           dc.opts.to,
			Commit:       dc.opts.commit,
			MergeBase:    dc.mergeBase(ctx),
			Strategy:     grouping.Strategy,
			Fallback:     grouping.Fallback,
			ChangedFiles: groupFiles(grouping.Changed),
		}
		for i, g := range grouping.Groups {
			groups.Groups = append(groups.Groups, delegate.Group{ID: i + 1, Label: g.Label, Files: groupFiles(g.Files)})
		}
		if err := delegate.WriteGroups(opts.outDir, groups); err != nil {
			return fmt.Errorf("write groups: %w", err)
		}
		status.Groups = len(groups.Groups)
		status.Fallback = grouping.Fallback
		status.GroupsFile = filepath.Join(opts.outDir, delegate.GroupsFile)
	}

	if opts.format == "json" {
		return writeDelegateJSON(status)
	}
	if status.NeedsAnswer {
		fmt.Printf("grouping: %d file(s) need a grouping answer; request written to %s\n", status.Files, status.Request)
		return nil
	}
	fmt.Printf("grouping: %d group(s) for %d file(s), strategy %s; written to %s\n", status.Groups, status.Files, status.Strategy, status.GroupsFile)
	if status.Fallback != "" {
		fmt.Printf("grouping: the answer was not used (%s); every file is its own group\n", status.Fallback)
	}
	return nil
}

func executeDelegateMerge(opts delegateGroupOptions) error {
	merged, err := delegate.Merge(opts.outDir)
	if err != nil {
		return fmt.Errorf("merge failed: %w", err)
	}
	if opts.format == "json" {
		return writeDelegateJSON(merged)
	}
	fmt.Printf("merge: %d comment(s) from %d of %d group(s); written to %s\n",
		len(merged.Comments), len(merged.Finished), merged.Groups, filepath.Join(opts.outDir, delegate.MergedFile))
	for _, u := range merged.Unfinished {
		fmt.Printf("merge: group %s unfinished: %s\n", delegate.GroupName(u.Group), u.Reason)
	}
	return nil
}

// groupingRequestMarkdown renders the grouping messages for a host agent to read.
func groupingRequestMarkdown(messages []llm.Message) string {
	var b strings.Builder
	for i, m := range messages {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n", m.Role, strings.TrimRight(fmt.Sprint(m.Content), "\n"))
	}
	return b.String()
}

// groupFiles converts the agent's file entries to the ones groups.json holds.
func groupFiles(files []agent.DelegateFile) []delegate.GroupFile {
	out := make([]delegate.GroupFile, 0, len(files))
	for _, f := range files {
		out = append(out, delegate.GroupFile{Path: f.Path, Status: f.Status, Insertions: f.Insertions, Deletions: f.Deletions})
	}
	return out
}
