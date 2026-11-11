// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/stdout"
)

// DelegateFile is one changed file as delegation mode reports it.
type DelegateFile struct {
	Path       string
	Status     string
	Insertions int64
	Deletions  int64
}

// DelegateGroup is one review group of a delegated run.
type DelegateGroup struct {
	Label string
	Files []DelegateFile
}

// DelegateGrouping is the partition a review of the same change set dispatches,
// produced without OCR calling a model: either the groups, or the request a
// host agent has to answer before the groups exist.
type DelegateGrouping struct {
	// Strategy is the template's grouping decision: "llm", "bundle_all" or "per_file".
	Strategy string
	// Request holds the grouping messages a review sends its model. It is set
	// only while Strategy is "llm" and no answer has been applied.
	Request []llm.Message
	// Groups is the partition; nil while Request is waiting for its answer.
	Groups []DelegateGroup
	// Fallback says why an answer was not used and every file got its own
	// group; empty when the answer was used or none was needed.
	Fallback string
	// Changed lists every file a reviewer is told about, deletions included.
	Changed []DelegateFile
}

// errGroupingAnswerPending stops groupDiffs at the request when no answer exists yet.
var errGroupingAnswerPending = errors.New("grouping answer pending")

// groupingStandIn takes the place of the model in groupDiffs: it records the
// request and replies with the host agent's answer when there is one.
type groupingStandIn struct {
	answer  *string
	request []llm.Message
}

// CompletionsWithCtx records the grouping request and returns the stored answer.
func (s *groupingStandIn) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.request = req.Messages
	if s.answer == nil {
		return nil, errGroupingAnswerPending
	}
	content := *s.answer
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Role: "assistant", Content: &content}}}}, nil
}

// PlanDelegateGrouping reports how a review partitions args' change set: the
// groups when the template decides without a model, else the request to answer.
func PlanDelegateGrouping(ctx context.Context, args Args, tpl template.Template) (*DelegateGrouping, error) {
	return delegateGrouping(ctx, args, tpl, nil)
}

// ApplyDelegateGrouping partitions args' change set from a host agent's answer,
// with the parse, the guards and the per-file fallback a review applies.
func ApplyDelegateGrouping(ctx context.Context, args Args, tpl template.Template, answer string) (*DelegateGrouping, error) {
	return delegateGrouping(ctx, args, tpl, &answer)
}

// delegateGrouping loads the files delegate preview lists and partitions them.
func delegateGrouping(ctx context.Context, args Args, tpl template.Template, answer *string) (*DelegateGrouping, error) {
	// groupDiffs logs its decision for a terminal review; the command reports its own.
	defer stdout.Quiet()()

	// args carries no Template, as in Preview: OCR's per-file size ceiling does
	// not bind a host agent, so the files grouped are the files preview lists.
	a := &Agent{args: args}
	if _, err := a.loadPreviewDiffs(ctx); err != nil {
		return nil, fmt.Errorf("load diffs: %w", err)
	}
	retained, _ := summarizeSelection(a.selectFiles(a.diffs))
	return groupRetainedDiffs(ctx, retained, args.Model, tpl, answer), nil
}

// groupRetainedDiffs runs groupDiffs with groupingStandIn where the model sits,
// so a delegated partition and a review's come from one implementation.
func groupRetainedDiffs(ctx context.Context, retained []model.Diff, modelName string, tpl template.Template, answer *string) *DelegateGrouping {
	// Deletions are told to reviewers but never dispatched, as in dispatchSubtasks.
	var diffs []model.Diff
	for _, d := range retained {
		if !d.IsDeleted {
			diffs = append(diffs, d)
		}
	}

	standIn := &groupingStandIn{answer: answer}
	result := groupDiffs(ctx, diffs, standIn, modelName, tpl, llmloop.PromptTokenLimit(tpl.MaxTokens), nil)

	out := &DelegateGrouping{Changed: delegateFiles(retained)}
	if standIn.request == nil {
		// No request was made: the template partitioned the files itself.
		out.Strategy = localGroupingStrategy(tpl, diffs).String()
		out.Groups = delegateGroups(result.groups)
		return out
	}
	out.Strategy = template.GroupingViaLLM.String()
	if answer == nil {
		out.Request = standIn.request
		return out
	}
	out.Groups = delegateGroups(result.groups)
	if strings.TrimSpace(*answer) == "" {
		out.Fallback = "the grouping answer is empty"
	} else if _, err := parseGroupingResponse(*answer, diffs); err != nil {
		out.Fallback = err.Error()
	}
	return out
}

// localGroupingStrategy names the partition groupDiffs chose without a request.
func localGroupingStrategy(tpl template.Template, diffs []model.Diff) template.GroupingStrategy {
	if len(diffs) <= 1 {
		return template.GroupingPerFile
	}
	totalChanged, _ := diffsChurn(diffs)
	if strategy := tpl.GroupingPlan(len(diffs), totalChanged); strategy != template.GroupingViaLLM {
		return strategy
	}
	// The template asked for a model but ships no grouping task.
	return template.GroupingPerFile
}

// delegateFiles converts diffs to the file entries delegate preview prints.
func delegateFiles(diffs []model.Diff) []DelegateFile {
	files := make([]DelegateFile, 0, len(diffs))
	for _, d := range diffs {
		files = append(files, DelegateFile{
			Path:       effectivePath(d),
			Status:     diffStatus(d),
			Insertions: d.Insertions,
			Deletions:  d.Deletions,
		})
	}
	return files
}

// delegateGroups converts review groups to their delegated form.
func delegateGroups(groups []FileGroup) []DelegateGroup {
	out := make([]DelegateGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, DelegateGroup{Label: g.Label, Files: delegateFiles(g.Diffs)})
	}
	return out
}
