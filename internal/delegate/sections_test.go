// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package delegate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/rules"
)

const terraformLike = `> Favor precision over recall.

#### Hardcoded Secrets and Credentials
- A literal password

#### State and Lifecycle
- A committed state file
`

const kotlinLike = "### 1. Null Safety\n- Avoid !!\n\n#### Dead Code\n- Unused vals\n\n### 2. Coroutines\n- Use structured concurrency\n"

const nushellLike = "# Nushell Script Review Checklist\n\nIntro line.\n\n## 1. Security (Critical)\n\n### Code injection\n- no eval\n\n## 2. Correctness\n\n### Type safety\n- annotate\n"

const fencedLike = "#### Memory\n```c\n#### not a heading\n```\n\n#### Strings\n- use snprintf\n"

func TestSections(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"level four sections", terraformLike, []string{"Hardcoded Secrets and Credentials", "State and Lifecycle"}},
		{"level three sections keep a nested level four inside", kotlinLike, []string{"1. Null Safety", "2. Coroutines"}},
		{"a lone title is shared text", nushellLike, []string{"1. Security (Critical)", "2. Correctness"}},
		{"a heading inside a code fence is code", fencedLike, []string{"Memory", "Strings"}},
		{"no heading is one General section", "Check JSON files for spelling errors in json-keys.", []string{GeneralSection}},
		{"one heading names the one section", "#### Only\n- one rule\n", []string{"Only"}},
		{"empty rule has no section", "  \n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sections(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Sections() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKeepSection(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		heading string
		want    string
		ok      bool
	}{
		{
			name: "keeps the preamble and the one section", text: terraformLike, heading: "State and Lifecycle",
			want: "> Favor precision over recall.\n\n#### State and Lifecycle\n- A committed state file", ok: true,
		},
		{
			name: "keeps a nested heading with its section", text: kotlinLike, heading: "1. Null Safety",
			want: "### 1. Null Safety\n- Avoid !!\n\n#### Dead Code\n- Unused vals", ok: true,
		},
		{
			name: "keeps the title as shared text", text: nushellLike, heading: "2. Correctness",
			want: "# Nushell Script Review Checklist\n\nIntro line.\n\n## 2. Correctness\n\n### Type safety\n- annotate", ok: true,
		},
		{
			name: "fenced lines stay in their section", text: fencedLike, heading: "Memory",
			want: "#### Memory\n```c\n#### not a heading\n```", ok: true,
		},
		{
			name: "a headingless rule is whole under General", text: "Check yaml keys.", heading: GeneralSection,
			want: "Check yaml keys.", ok: true,
		},
		{name: "a missing section is not kept", text: terraformLike, heading: "Performance", ok: false},
		{name: "General is not a section of a sectioned rule", text: terraformLike, heading: GeneralSection, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := KeepSection(tt.text, tt.heading)
			if ok != tt.ok || got != tt.want {
				t.Errorf("KeepSection() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestFilterSection(t *testing.T) {
	resolver := &stubDetailResolver{mapping: map[string]rules.RuleDetail{
		"main.tf":    {Rule: terraformLike, Source: "system", Pattern: "**/*.tf"},
		"startup.sh": {Rule: "#### Correctness\n- logic\n\n#### Security\n- secrets\n", Source: "system", Pattern: "default"},
	}}
	groups := GroupRules(resolver, []string{"main.tf", "startup.sh"})
	if got := groups[0].Sections(); !reflect.DeepEqual(got, []string{"Hardcoded Secrets and Credentials", "State and Lifecycle"}) {
		t.Fatalf("group sections = %q", got)
	}

	kept := FilterSection(groups, "Security")
	if len(kept) != 1 || kept[0].ID != 2 || !reflect.DeepEqual(kept[0].Files, []string{"startup.sh"}) {
		t.Fatalf("FilterSection kept %+v, want only group 2 with startup.sh", kept)
	}
	if kept[0].Text != "#### Security\n- secrets" {
		t.Errorf("kept text = %q", kept[0].Text)
	}
	if strings.Contains(groups[1].Text, "Security") == false || !strings.Contains(groups[1].Text, "Correctness") {
		t.Errorf("FilterSection changed the input groups: %q", groups[1].Text)
	}
	if got := FilterSection(groups, "Performance"); len(got) != 0 {
		t.Errorf("FilterSection on a missing section = %+v, want none", got)
	}
}
