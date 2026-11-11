// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/delegate"
)

// groupTestRepo commits n Go files of 60 lines each on top of a base commit,
// a change set past the shipped grouping thresholds when n is 4 or more.
func groupTestRepo(t *testing.T, n int) string {
	t.Helper()
	freshOCRHome(t)
	dir := initTestGitRepo(t)
	for i := 0; i < n; i++ {
		body := "package pkg\n" + strings.Repeat("// line\n", 60)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(body), 0o644); err != nil {
			t.Fatalf("write file%d.go: %v", i, err)
		}
	}
	gitCommitFile(t, dir, "last.go", "package pkg\n"+strings.Repeat("// line\n", 60), "change")
	return dir
}

func runDelegateGroup(t *testing.T, opts delegateGroupOptions, applying bool) delegateGroupJSON {
	t.Helper()
	opts.format = "json"
	var status delegateGroupJSON
	out := captureDelegateStdout(t, func() {
		if err := executeDelegateGroup(opts, applying); err != nil {
			t.Fatalf("executeDelegateGroup: %v", err)
		}
	})
	if err := json.Unmarshal(out, &status); err != nil {
		t.Fatalf("status is not JSON: %v\n%s", err, out)
	}
	return status
}

func TestDelegateGroupWritesTheRequestThenAppliesTheAnswer(t *testing.T) {
	dir := groupTestRepo(t, 5)
	out := filepath.Join(t.TempDir(), "review")
	opts := delegateGroupOptions{delegateOptions: delegateOptions{repoDir: dir, from: "HEAD~1", to: "HEAD"}, outDir: out}

	status := runDelegateGroup(t, opts, false)
	if !status.NeedsAnswer || status.Strategy != "llm" || status.Files != 6 {
		t.Fatalf("plan status = %+v, want an answer needed for 6 files", status)
	}
	request, err := os.ReadFile(filepath.Join(out, delegate.GroupingRequestFile))
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	for _, want := range []string{"## system", "## user", "file grouping assistant", "[5] ADDED   last.go (+61/-0)"} {
		if !strings.Contains(string(request), want) {
			t.Errorf("request lacks %q:\n%s", want, request)
		}
	}
	if _, err := os.Stat(filepath.Join(out, delegate.GroupsFile)); !os.IsNotExist(err) {
		t.Errorf("groups.json exists before any answer (err=%v)", err)
	}

	opts.apply = filepath.Join(out, "grouping.json")
	if err := os.WriteFile(opts.apply, []byte(`[{"label":"first","files":[0,1,2]},{"label":"rest","files":[3,4,5]}]`), 0o644); err != nil {
		t.Fatalf("write answer: %v", err)
	}
	status = runDelegateGroup(t, opts, true)
	if status.NeedsAnswer || status.Groups != 2 || status.Fallback != "" {
		t.Fatalf("apply status = %+v, want 2 groups and no fallback", status)
	}
	groups, err := delegate.ReadGroups(out)
	if err != nil {
		t.Fatalf("ReadGroups: %v", err)
	}
	if groups.Mode != "range" || groups.MergeBase == "" || len(groups.Groups) != 2 || groups.Groups[1].Label != "rest" {
		t.Errorf("groups.json = %+v", groups)
	}
	for _, name := range []string{"01.json", "02.json"} {
		if _, err := os.Stat(filepath.Join(out, delegate.GroupsDir, name)); err != nil {
			t.Errorf("slice %s: %v", name, err)
		}
	}
}

func TestDelegateGroupFallsBackPerFileOnAMissingAnswer(t *testing.T) {
	dir := groupTestRepo(t, 5)
	out := filepath.Join(t.TempDir(), "review")
	opts := delegateGroupOptions{
		delegateOptions: delegateOptions{repoDir: dir, from: "HEAD~1", to: "HEAD"},
		outDir:          out,
		apply:           filepath.Join(out, "never-written.json"),
	}

	status := runDelegateGroup(t, opts, true)

	if status.Groups != 6 || status.Fallback == "" {
		t.Fatalf("status = %+v, want 6 per-file groups and a fallback reason", status)
	}
	groups, err := delegate.ReadGroups(out)
	if err != nil {
		t.Fatalf("ReadGroups: %v", err)
	}
	if groups.Fallback == "" {
		t.Error("groups.json does not record that the answer was not used")
	}
}

func TestDelegateGroupNeedsNoAnswerForASmallChange(t *testing.T) {
	freshOCRHome(t)
	dir := initTestGitRepo(t)
	gitCommitFile(t, dir, "b.go", "package b\n", "small change")
	out := filepath.Join(t.TempDir(), "review")
	opts := delegateGroupOptions{delegateOptions: delegateOptions{repoDir: dir, from: "HEAD~1", to: "HEAD"}, outDir: out}

	status := runDelegateGroup(t, opts, false)

	if status.NeedsAnswer || status.Groups != 1 || status.Strategy == "llm" {
		t.Fatalf("status = %+v, want one group with no answer needed", status)
	}
	if _, err := os.Stat(filepath.Join(out, delegate.GroupingRequestFile)); !os.IsNotExist(err) {
		t.Errorf("a grouping request was written for a change a review groups itself (err=%v)", err)
	}
}

func TestDelegateMergeReportsWhatEachGroupLeft(t *testing.T) {
	dir := groupTestRepo(t, 5)
	out := filepath.Join(t.TempDir(), "review")
	opts := delegateGroupOptions{delegateOptions: delegateOptions{repoDir: dir, from: "HEAD~1", to: "HEAD"}, outDir: out}
	opts.apply = filepath.Join(t.TempDir(), "grouping.json")
	if err := os.WriteFile(opts.apply, []byte(`[{"label":"first","files":[0,1,2]},{"label":"rest","files":[3,4,5]}]`), 0o644); err != nil {
		t.Fatalf("write answer: %v", err)
	}
	runDelegateGroup(t, opts, true)
	comments := `{"comments":[{"path":"file0.go","content":"unused import","start_line":2,"severity":"low"}]}`
	if err := os.WriteFile(filepath.Join(out, delegate.CommentsFile(1)), []byte(comments), 0o644); err != nil {
		t.Fatalf("write comments: %v", err)
	}

	var merged delegate.Merged
	stdout := captureDelegateStdout(t, func() {
		if err := executeDelegateMerge(delegateGroupOptions{delegateOptions: delegateOptions{format: "json"}, outDir: out}); err != nil {
			t.Fatalf("executeDelegateMerge: %v", err)
		}
	})
	if err := json.Unmarshal(stdout, &merged); err != nil {
		t.Fatalf("merge output is not JSON: %v\n%s", err, stdout)
	}
	if len(merged.Comments) != 1 || len(merged.Finished) != 1 || len(merged.Unfinished) != 1 || merged.Unfinished[0].Group != 2 {
		t.Errorf("merged = %+v, want one comment, group 1 finished and group 2 unfinished", merged)
	}
	if merged.TotalFiles != 6 || merged.ReviewedFiles != 3 {
		t.Errorf("total=%d reviewed=%d, want 6 and 3", merged.TotalFiles, merged.ReviewedFiles)
	}
}

func TestDelegateMergeFailsWithoutGroups(t *testing.T) {
	err := executeDelegateMerge(delegateGroupOptions{delegateOptions: delegateOptions{format: "text"}, outDir: t.TempDir()})
	if err == nil {
		t.Error("merge succeeded in a folder with no groups.json")
	}
}
