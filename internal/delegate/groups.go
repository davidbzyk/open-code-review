// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package delegate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// GroupsSchemaVersion versions every file an orchestrated delegation writes.
const GroupsSchemaVersion = "1"

// Names of the files an orchestrated delegation keeps in its output folder.
const (
	GroupingRequestFile = "grouping-request.md"
	GroupsFile          = "groups.json"
	GroupsDir           = "groups"
	MergedFile          = "merged.json"
)

// GroupFile is one changed file of a review group.
type GroupFile struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int64  `json:"insertions"`
	Deletions  int64  `json:"deletions"`
}

// Group is one review group, numbered from 1 in dispatch order.
type Group struct {
	ID    int         `json:"id"`
	Label string      `json:"label"`
	Files []GroupFile `json:"files"`
}

// Groups is groups.json: the partition every reviewer and the merge read.
type Groups struct {
	SchemaVersion string      `json:"schema_version"`
	Mode          string      `json:"mode"`
	From          string      `json:"from,omitempty"`
	To            string      `json:"to,omitempty"`
	Commit        string      `json:"commit,omitempty"`
	MergeBase     string      `json:"merge_base,omitempty"`
	Strategy      string      `json:"strategy"`
	Fallback      string      `json:"fallback,omitempty"`
	Groups        []Group     `json:"groups"`
	ChangedFiles  []GroupFile `json:"changed_files"`
}

// GroupSlice is groups/<NN>.json: what one reviewer is given.
type GroupSlice struct {
	SchemaVersion     string      `json:"schema_version"`
	Group             int         `json:"group"`
	Of                int         `json:"of"`
	Label             string      `json:"label"`
	Mode              string      `json:"mode"`
	From              string      `json:"from,omitempty"`
	To                string      `json:"to,omitempty"`
	Commit            string      `json:"commit,omitempty"`
	MergeBase         string      `json:"merge_base,omitempty"`
	Files             []GroupFile `json:"files"`
	OtherChangedFiles []GroupFile `json:"other_changed_files"`
}

// Unfinished names a group whose reviewer left no usable comments file.
type Unfinished struct {
	Group  int    `json:"group"`
	Reason string `json:"reason"`
}

// Merged is merged.json: every reviewer's comments and what was covered.
type Merged struct {
	SchemaVersion string           `json:"schema_version"`
	Groups        int              `json:"groups"`
	Finished      []int            `json:"finished"`
	Unfinished    []Unfinished     `json:"unfinished"`
	TotalFiles    int              `json:"total_files"`
	ReviewedFiles int              `json:"reviewed_files"`
	Comments      []map[string]any `json:"comments"`
}

// GroupName is a group's two-digit name in file names: 1 is "01".
func GroupName(id int) string {
	return fmt.Sprintf("%02d", id)
}

// CommentsFile is the file name a group's reviewer writes its comments to.
func CommentsFile(id int) string {
	return "comments." + GroupName(id) + ".json"
}

// WriteGroups writes groups.json and one slice per group under dir, replacing
// the slices of an earlier partition.
func WriteGroups(dir string, g Groups) error {
	g.SchemaVersion = GroupsSchemaVersion
	if g.Groups == nil {
		g.Groups = []Group{}
	}
	if g.ChangedFiles == nil {
		g.ChangedFiles = []GroupFile{}
	}
	slices := filepath.Join(dir, GroupsDir)
	if err := os.RemoveAll(slices); err != nil {
		return err
	}
	if err := os.MkdirAll(slices, 0o755); err != nil {
		return err
	}
	for _, group := range g.Groups {
		slice := GroupSlice{
			SchemaVersion:     GroupsSchemaVersion,
			Group:             group.ID,
			Of:                len(g.Groups),
			Label:             group.Label,
			Mode:              g.Mode,
			From:              g.From,
			To:                g.To,
			Commit:            g.Commit,
			MergeBase:         g.MergeBase,
			Files:             group.Files,
			OtherChangedFiles: otherFiles(g.ChangedFiles, group.Files),
		}
		if err := writeJSON(filepath.Join(slices, GroupName(group.ID)+".json"), slice); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(dir, GroupsFile), g)
}

// ReadGroups reads the groups.json a grouping wrote under dir.
func ReadGroups(dir string) (Groups, error) {
	var g Groups
	data, err := os.ReadFile(filepath.Join(dir, GroupsFile))
	if err != nil {
		return g, err
	}
	if err := json.Unmarshal(data, &g); err != nil {
		return g, fmt.Errorf("%s: %w", GroupsFile, err)
	}
	return g, nil
}

// Merge gathers each group's comments file under dir into merged.json. A group
// with no usable file is listed as unfinished and never fails the merge.
func Merge(dir string) (Merged, error) {
	g, err := ReadGroups(dir)
	if err != nil {
		return Merged{}, err
	}
	m := Merged{
		SchemaVersion: GroupsSchemaVersion,
		Groups:        len(g.Groups),
		Finished:      []int{},
		Unfinished:    []Unfinished{},
		Comments:      []map[string]any{},
	}
	for _, group := range g.Groups {
		m.TotalFiles += len(group.Files)
		comments, err := readComments(filepath.Join(dir, CommentsFile(group.ID)))
		if err != nil {
			m.Unfinished = append(m.Unfinished, Unfinished{Group: group.ID, Reason: err.Error()})
			continue
		}
		m.Finished = append(m.Finished, group.ID)
		m.ReviewedFiles += len(group.Files)
		for _, comment := range comments {
			comment["group"] = group.ID
			m.Comments = append(m.Comments, comment)
		}
	}
	return m, writeJSON(filepath.Join(dir, MergedFile), m)
}

// readComments reads one reviewer's `{"comments": [...]}` file; an entry that
// is not an object is left out.
func readComments(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("no comments file")
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Comments *[]json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("comments file does not parse: %w", err)
	}
	if doc.Comments == nil {
		return nil, errors.New("comments file has no comments list")
	}
	comments := make([]map[string]any, 0, len(*doc.Comments))
	for _, raw := range *doc.Comments {
		var comment map[string]any
		if json.Unmarshal(raw, &comment) == nil && comment != nil {
			comments = append(comments, comment)
		}
	}
	return comments, nil
}

// otherFiles returns the changed files that are not in the group, in order.
func otherFiles(changed, group []GroupFile) []GroupFile {
	in := make(map[string]bool, len(group))
	for _, f := range group {
		in[f.Path] = true
	}
	others := make([]GroupFile, 0, len(changed))
	for _, f := range changed {
		if !in[f.Path] {
			others = append(others, f)
		}
	}
	return others
}

// writeJSON writes value to path as indented JSON with a trailing newline.
func writeJSON(path string, value any) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	// Comments quote code, so `<` and `&` stay readable for the agent that reads them.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
