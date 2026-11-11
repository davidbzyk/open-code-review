// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package delegate

import (
	"regexp"
	"strings"
)

// GeneralSection names the one section of a rule that has no headings.
const GeneralSection = "General"

var headingPattern = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*(?:[ \t]#+)?[ \t]*$`)

// ruleLine is one line of a rule and the section it belongs to; section -1 is shared text.
type ruleLine struct {
	text    string
	section int
}

// splitSections cuts a rule into its sections: the runs under each heading of the
// shallowest level that occurs at least twice, or under its only heading. Lines in no
// section (a title, a preamble) are shared. Headings inside code fences are code.
func splitSections(text string) ([]string, []ruleLine) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	levels := make([]int, len(lines))
	titles := make([]string, len(lines))
	counts := map[int]int{}
	fenced := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if m := headingPattern.FindStringSubmatch(line); m != nil && !fenced && m[2] != "" {
			levels[i], titles[i] = len(m[1]), m[2]
			counts[len(m[1])]++
		}
	}
	level := 0
	for l := 1; l <= 6; l++ {
		if counts[l] >= 2 {
			level = l
			break
		}
	}
	if level == 0 && len(counts) == 1 {
		for l := range counts {
			level = l
		}
	}
	if level == 0 {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		out := make([]ruleLine, len(lines))
		for i, line := range lines {
			out[i] = ruleLine{text: line, section: 0}
		}
		return []string{GeneralSection}, out
	}

	var names []string
	index := map[string]int{}
	current := -1
	out := make([]ruleLine, len(lines))
	for i, line := range lines {
		if levels[i] != 0 && levels[i] <= level {
			current = -1
			if levels[i] == level {
				idx, seen := index[titles[i]]
				if !seen {
					idx = len(names)
					index[titles[i]] = idx
					names = append(names, titles[i])
				}
				current = idx
			}
		}
		out[i] = ruleLine{text: line, section: current}
	}
	return names, out
}

// Sections lists a rule's section headings in order (GeneralSection for a rule with none).
func Sections(text string) []string {
	names, _ := splitSections(text)
	return names
}

// KeepSection cuts a rule to its shared text and the named section; false when it has no such section.
func KeepSection(text, heading string) (string, bool) {
	names, lines := splitSections(text)
	want := -2
	for i, name := range names {
		if name == heading {
			want = i
		}
	}
	if want == -2 {
		return "", false
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.section == -1 || line.section == want {
			kept = append(kept, line.text)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n "), true
}

// Sections lists the section headings of the group's rule.
func (g RuleGroup) Sections() []string {
	return Sections(g.Text)
}

// FilterSection keeps the groups whose rule has the named section, each cut to that section.
func FilterSection(groups []RuleGroup, heading string) []RuleGroup {
	var kept []RuleGroup
	for _, group := range groups {
		text, ok := KeepSection(group.Text, heading)
		if !ok {
			continue
		}
		group.Text = text
		group.Files = append([]string(nil), group.Files...)
		kept = append(kept, group)
	}
	return kept
}
