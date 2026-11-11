// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package delegate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	orchestratorSkill       = "skills/open-code-review-orchestrator/SKILL.md"
	orchestratorPluginSkill = "plugins/open-code-review/skills/open-code-review-orchestrator/SKILL.md"
)

// readRepoFile reads a file by its path from the repository root.
func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func TestOrchestratorSkillCopiesSayTheSame(t *testing.T) {
	canonical := readRepoFile(t, orchestratorSkill)
	plugin := readRepoFile(t, orchestratorPluginSkill)

	note := regexp.MustCompile(`(?s)This Codex plugin skill intentionally mirrors.*?plugin subtree\.\n\n`)
	if !note.MatchString(plugin) {
		t.Fatalf("%s lacks the note that it mirrors the canonical skill", orchestratorPluginSkill)
	}
	if got := note.ReplaceAllString(plugin, ""); got != canonical {
		t.Errorf("%s differs from %s beyond the mirror note", orchestratorPluginSkill, orchestratorSkill)
	}
}

func TestOrchestratorSkillNamesTheFilesTheCommandsWrite(t *testing.T) {
	skill := readRepoFile(t, orchestratorSkill)
	for _, name := range []string{
		"<out>/" + GroupingRequestFile,
		"<out>/" + GroupsFile,
		"<out>/" + GroupsDir + "/<n>.json",
		"<out>/" + MergedFile,
		"<out>/comments.<n>.json",
		"ocr delegate group <refs> <rule flags> --out <out>\n",
		"ocr delegate group <refs> <rule flags> --out <out> --apply <out>/grouping.json\n",
		"ocr delegate merge --out <out>\n",
	} {
		if !strings.Contains(skill, name) {
			t.Errorf("the orchestrator skill no longer names %q", name)
		}
	}
	if CommentsFile(3) != "comments.03.json" || GroupName(12) != "12" {
		t.Errorf("group names moved: %s, %s", CommentsFile(3), GroupName(12))
	}
}
