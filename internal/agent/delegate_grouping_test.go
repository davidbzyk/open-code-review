// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/model"
)

// delegateTestDiffs is a change set past the shipped grouping thresholds.
func delegateTestDiffs(n int) []model.Diff {
	diffs := make([]model.Diff, 0, n)
	for i := 0; i < n; i++ {
		diffs = append(diffs, model.Diff{
			OldPath:    fmt.Sprintf("pkg/file%d.go", i),
			NewPath:    fmt.Sprintf("pkg/file%d.go", i),
			Insertions: 60,
			Diff:       "+line\n",
		})
	}
	return diffs
}

// reviewGroups is the partition a review reaches when its model replies with answer.
func reviewGroups(t *testing.T, diffs []model.Diff, tpl template.Template, answer string) ([]FileGroup, *fakeGroupingClient) {
	t.Helper()
	client := &fakeGroupingClient{response: answer}
	result := groupDiffs(context.Background(), diffs, client, "fake", tpl, llmloop.PromptTokenLimit(tpl.MaxTokens), nil)
	return result.groups, client
}

func TestDelegateGroupingRequestIsWhatAReviewSends(t *testing.T) {
	tpl, err := template.LoadDefault()
	if err != nil {
		t.Fatalf("load default template: %v", err)
	}
	diffs := delegateTestDiffs(6)

	_, client := reviewGroups(t, diffs, *tpl, `[]`)
	if !client.called {
		t.Fatal("a review of 6 files made no grouping call; the test needs a change set past the threshold")
	}

	got := groupRetainedDiffs(context.Background(), diffs, "fake", *tpl, nil)
	if got.Strategy != "llm" {
		t.Fatalf("strategy = %q, want llm", got.Strategy)
	}
	if got.Groups != nil {
		t.Fatalf("groups = %v before any answer, want none", got.Groups)
	}
	if !reflect.DeepEqual(got.Request, client.gotReq.Messages) {
		t.Errorf("delegated request differs from the review's:\n got %v\nwant %v", got.Request, client.gotReq.Messages)
	}
}

func TestDelegateGroupsFromAnAnswerAreTheReviewsGroups(t *testing.T) {
	tpl, err := template.LoadDefault()
	if err != nil {
		t.Fatalf("load default template: %v", err)
	}
	// 14 files: one answer group of 12 has to be split by the 10-file guard,
	// one index repeats, one is out of range and two files are left unassigned.
	diffs := delegateTestDiffs(14)
	answer := "```json\n" + `[{"label":"big","files":[0,1,2,3,4,5,6,7,8,9,10,11]},{"label":"dup","files":[11,99]}]` + "\n```"

	want, _ := reviewGroups(t, diffs, *tpl, answer)
	got := groupRetainedDiffs(context.Background(), diffs, "fake", *tpl, &answer)

	if got.Fallback != "" {
		t.Fatalf("fallback = %q for an answer a review accepts", got.Fallback)
	}
	if !reflect.DeepEqual(got.Groups, delegateGroups(want)) {
		t.Errorf("delegated groups differ from the review's:\n got %v\nwant %v", got.Groups, delegateGroups(want))
	}
	if len(got.Groups) != 4 {
		t.Errorf("got %d groups, want 4 (10 + 2 split, then two unassigned files)", len(got.Groups))
	}
}

func TestDelegateGroupingFallsBackPerFileLikeAReview(t *testing.T) {
	tpl, err := template.LoadDefault()
	if err != nil {
		t.Fatalf("load default template: %v", err)
	}
	diffs := delegateTestDiffs(6)
	for name, answer := range map[string]string{"unparseable": "the files look related", "empty": "  \n"} {
		t.Run(name, func(t *testing.T) {
			want, _ := reviewGroups(t, diffs, *tpl, answer)
			got := groupRetainedDiffs(context.Background(), diffs, "fake", *tpl, &answer)
			if got.Fallback == "" {
				t.Error("fallback is empty for an answer a review cannot use")
			}
			if len(got.Groups) != len(diffs) {
				t.Errorf("got %d groups, want one per file (%d)", len(got.Groups), len(diffs))
			}
			if !reflect.DeepEqual(got.Groups, delegateGroups(want)) {
				t.Errorf("delegated groups differ from the review's fallback")
			}
		})
	}
}

func TestDelegateGroupingNeedsNoAnswerBelowTheThreshold(t *testing.T) {
	tpl, err := template.LoadDefault()
	if err != nil {
		t.Fatalf("load default template: %v", err)
	}
	diffs := delegateTestDiffs(3)
	diffs[0].Insertions, diffs[1].Insertions, diffs[2].Insertions = 5, 5, 5

	want, client := reviewGroups(t, diffs, *tpl, `[]`)
	if client.called {
		t.Fatal("a review of 3 small files made a grouping call; the test needs a change set under the threshold")
	}

	ignored := `[{"label":"x","files":[0]}]`
	for name, answer := range map[string]*string{"plan": nil, "apply": &ignored} {
		t.Run(name, func(t *testing.T) {
			got := groupRetainedDiffs(context.Background(), diffs, "fake", *tpl, answer)
			if got.Request != nil {
				t.Error("a request was written although a review makes no grouping call")
			}
			if got.Strategy != "bundle_all" {
				t.Errorf("strategy = %q, want bundle_all", got.Strategy)
			}
			if !reflect.DeepEqual(got.Groups, delegateGroups(want)) {
				t.Errorf("delegated groups differ from the review's:\n got %v\nwant %v", got.Groups, delegateGroups(want))
			}
		})
	}
}

func TestDelegateGroupingListsDeletionsButNeverGroupsThem(t *testing.T) {
	tpl, err := template.LoadDefault()
	if err != nil {
		t.Fatalf("load default template: %v", err)
	}
	diffs := delegateTestDiffs(5)
	diffs = append(diffs, model.Diff{OldPath: "pkg/gone.go", NewPath: "/dev/null", IsDeleted: true, Deletions: 9})
	answer := `[{"label":"all","files":[0,1,2,3,4]}]`

	got := groupRetainedDiffs(context.Background(), diffs, "fake", *tpl, &answer)

	if len(got.Changed) != 6 || got.Changed[5].Path != "pkg/gone.go" || got.Changed[5].Status != "deleted" {
		t.Errorf("changed files = %v, want the deletion listed last", got.Changed)
	}
	for _, g := range got.Groups {
		for _, f := range g.Files {
			if f.Path == "pkg/gone.go" {
				t.Error("the deleted file was put in a review group")
			}
		}
	}
}
