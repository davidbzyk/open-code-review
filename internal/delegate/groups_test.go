// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package delegate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testGroups is a three-group partition of four files, one of them deleted.
func testGroups() Groups {
	a := GroupFile{Path: "api/handler.go", Status: "modified", Insertions: 12, Deletions: 3}
	b := GroupFile{Path: "api/service.go", Status: "added", Insertions: 40}
	c := GroupFile{Path: "web/page.tsx", Status: "modified", Insertions: 7, Deletions: 1}
	gone := GroupFile{Path: "api/old.go", Status: "deleted", Deletions: 20}
	return Groups{
		Mode: "range", From: "base", To: "head", MergeBase: "base", Strategy: "llm",
		Groups: []Group{
			{ID: 1, Label: "api", Files: []GroupFile{a, b}},
			{ID: 2, Label: "web", Files: []GroupFile{c}},
		},
		ChangedFiles: []GroupFile{a, b, c, gone},
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestWriteGroupsGivesEachReviewerItsSlice(t *testing.T) {
	dir := t.TempDir()
	if err := WriteGroups(dir, testGroups()); err != nil {
		t.Fatalf("WriteGroups: %v", err)
	}

	got, err := ReadGroups(dir)
	if err != nil {
		t.Fatalf("ReadGroups: %v", err)
	}
	want := testGroups()
	want.SchemaVersion = GroupsSchemaVersion
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups.json round trip:\n got %+v\nwant %+v", got, want)
	}

	data, err := os.ReadFile(filepath.Join(dir, GroupsDir, "02.json"))
	if err != nil {
		t.Fatalf("read slice: %v", err)
	}
	var slice GroupSlice
	if err := json.Unmarshal(data, &slice); err != nil {
		t.Fatalf("slice does not parse: %v", err)
	}
	if slice.Group != 2 || slice.Of != 2 || slice.Label != "web" || slice.MergeBase != "base" {
		t.Errorf("slice header = %+v", slice)
	}
	if len(slice.Files) != 1 || slice.Files[0].Path != "web/page.tsx" {
		t.Errorf("slice files = %+v, want only web/page.tsx", slice.Files)
	}
	var others []string
	for _, f := range slice.OtherChangedFiles {
		others = append(others, f.Path)
	}
	if !reflect.DeepEqual(others, []string{"api/handler.go", "api/service.go", "api/old.go"}) {
		t.Errorf("other changed files = %v, want the rest of the change, deletion included", others)
	}
}

func TestWriteGroupsReplacesAnEarlierPartition(t *testing.T) {
	dir := t.TempDir()
	if err := WriteGroups(dir, testGroups()); err != nil {
		t.Fatalf("WriteGroups: %v", err)
	}
	one := testGroups()
	one.Groups = one.Groups[:1]
	if err := WriteGroups(dir, one); err != nil {
		t.Fatalf("WriteGroups again: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, GroupsDir, "02.json")); !os.IsNotExist(err) {
		t.Errorf("slice 02 of the earlier partition is still there (err=%v)", err)
	}
}

func TestMergeTagsCommentsAndListsUnfinishedGroups(t *testing.T) {
	dir := t.TempDir()
	g := testGroups()
	g.Groups = append(g.Groups, Group{ID: 3, Label: "broken", Files: []GroupFile{{Path: "x.go", Status: "added"}}},
		Group{ID: 4, Label: "no list", Files: []GroupFile{{Path: "y.go", Status: "added"}}})
	if err := WriteGroups(dir, g); err != nil {
		t.Fatalf("WriteGroups: %v", err)
	}
	writeFile(t, filepath.Join(dir, CommentsFile(1)),
		`{"comments":[{"path":"api/handler.go","content":"if a < b && c > d","start_line":4,"severity":"high"},"not an object",{"path":"api/service.go","content":"second"}]}`)
	// Group 2 wrote nothing; group 3 wrote text that is not JSON; group 4 has no list.
	writeFile(t, filepath.Join(dir, CommentsFile(3)), "I found no issues")
	writeFile(t, filepath.Join(dir, CommentsFile(4)), `{"findings":[]}`)

	merged, err := Merge(dir)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Groups != 4 || !reflect.DeepEqual(merged.Finished, []int{1}) {
		t.Errorf("groups=%d finished=%v, want 4 groups with only 1 finished", merged.Groups, merged.Finished)
	}
	var unfinished []int
	for _, u := range merged.Unfinished {
		unfinished = append(unfinished, u.Group)
		if u.Reason == "" {
			t.Errorf("group %d is unfinished with no reason", u.Group)
		}
	}
	if !reflect.DeepEqual(unfinished, []int{2, 3, 4}) {
		t.Errorf("unfinished = %v, want [2 3 4]", unfinished)
	}
	if merged.TotalFiles != 5 || merged.ReviewedFiles != 2 {
		t.Errorf("total=%d reviewed=%d, want 5 and 2", merged.TotalFiles, merged.ReviewedFiles)
	}
	if len(merged.Comments) != 2 {
		t.Fatalf("got %d comments, want 2 (the non-object entry is left out)", len(merged.Comments))
	}
	for _, c := range merged.Comments {
		if c["group"] != 1 {
			t.Errorf("comment %v is not tagged with group 1", c)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, MergedFile))
	if err != nil {
		t.Fatalf("read merged.json: %v", err)
	}
	if !strings.Contains(string(data), "if a < b && c > d") {
		t.Errorf("merged.json escaped the quoted code:\n%s", data)
	}
	var onDisk Merged
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("merged.json does not parse: %v", err)
	}
	if len(onDisk.Comments) != 2 || len(onDisk.Unfinished) != 3 {
		t.Errorf("merged.json holds %d comments and %d unfinished", len(onDisk.Comments), len(onDisk.Unfinished))
	}
}

func TestMergeWithoutGroupsIsAnError(t *testing.T) {
	if _, err := Merge(t.TempDir()); err == nil {
		t.Error("Merge succeeded in a folder with no groups.json")
	}
}
