// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgentCLI writes a script standing in for a coding-agent CLI. Its body sees
// $prompt (the instructions file) and $result (where the answer belongs), found
// the way a real agent finds them: by reading the one-line instruction.
func fakeAgentCLI(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-agent.sh")
	src := `#!/bin/bash
prompt=$(printf '%s' "$1" | sed -E 's/^Read the file (.*) and do exactly what it says\.$/\1/')
result=$(sed -n 's/^RESULT_FILE: //p' "$prompt")
` + body + "\n"
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return "bash " + script + " {prompt}"
}

func reportTools() []ToolDef {
	return []ToolDef{
		{Type: "function", Function: FunctionDef{Name: "code_comment", Description: "Report an issue.", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: FunctionDef{Name: "task_done", Description: "Finish.", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: FunctionDef{Name: "file_read", Description: "READ-A-FILE-DESCRIPTION", Parameters: map[string]any{"type": "object"}}},
	}
}

func reviewRequest() ChatRequest {
	return ChatRequest{
		Model: "some-model",
		Messages: []Message{
			NewTextMessage("system", "SYSTEM-RULES-TEXT"),
			NewTextMessage("user", "USER-DIFF-TEXT"),
		},
		Tools: reportTools(),
	}
}

func newFakeClient(t *testing.T, command string) *AgentCLIClient {
	t.Helper()
	t.Chdir(t.TempDir())
	return NewAgentCLIClient(ClientConfig{Model: "some-model", AgentCommand: command})
}

func TestAgentCLIReturnsTheToolCallsTheSessionReported(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `cat > "$result" <<'EOF'
{"tool_calls":[{"name":"code_comment","arguments":{"comments":[{"content":"bug"}]}},{"name":"task_done","arguments":{"state":"DONE"}}]}
EOF`))
	resp, err := client.CompletionsWithCtx(context.Background(), reviewRequest())
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(calls))
	}
	if calls[0].Function.Name != "code_comment" || calls[1].Function.Name != "task_done" {
		t.Fatalf("calls out of order: %+v", calls)
	}
	if calls[0].ID == "" || calls[0].ID == calls[1].ID {
		t.Fatalf("tool call ids must be unique and set: %q %q", calls[0].ID, calls[1].ID)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments are not JSON: %v", err)
	}
	if _, ok := args["comments"]; !ok {
		t.Fatalf("arguments lost: %v", args)
	}
}

func TestAgentCLIPromptCarriesTheConversationAndOnlyTheReportTools(t *testing.T) {
	saved := filepath.Join(t.TempDir(), "prompt-copy.md")
	client := newFakeClient(t, fakeAgentCLI(t, `cp "$prompt" "`+saved+`"
echo '{"tool_calls":[{"name":"task_done","arguments":{}}]}' > "$result"`))
	if _, err := client.CompletionsWithCtx(context.Background(), reviewRequest()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(raw)
	for _, want := range []string{"SYSTEM-RULES-TEXT", "USER-DIFF-TEXT", "code_comment", "task_done", "RESULT_FILE: "} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	if strings.Contains(prompt, "READ-A-FILE-DESCRIPTION") {
		t.Error("prompt lists a code-reading tool the CLI already has its own version of")
	}
}

func TestAgentCLIFinishesTheTaskWhenTheSessionOmitsTaskDone(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo '{"tool_calls":[{"name":"code_comment","arguments":{"comments":[]}}]}' > "$result"`))
	resp, err := client.CompletionsWithCtx(context.Background(), reviewRequest())
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 2 || calls[1].Function.Name != "task_done" {
		t.Fatalf("want code_comment then an added task_done, got %+v", calls)
	}
}

func TestAgentCLIDoesNotInventTaskDoneForOtherTools(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo '{"tool_calls":[{"name":"keep_all","arguments":{}}]}' > "$result"`))
	req := reviewRequest()
	req.Tools = []ToolDef{{Type: "function", Function: FunctionDef{Name: "keep_all", Parameters: map[string]any{"type": "object"}}}}
	resp, err := client.CompletionsWithCtx(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if calls := resp.ToolCalls(); len(calls) != 1 || calls[0].Function.Name != "keep_all" {
		t.Fatalf("got %+v", calls)
	}
}

func TestAgentCLIPlainReplyWhenTheRequestHasNoTools(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo '{"content":"plain answer"}' > "$result"`))
	req := reviewRequest()
	req.Tools = nil
	resp, err := client.CompletionsWithCtx(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content() != "plain answer" {
		t.Fatalf("got %q", resp.Content())
	}
}

func TestAgentCLINoAnswerFileReportsWhatTheCLISaid(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo 'AGY_ERROR: quota reached' >&2; exit 3`))
	_, err := client.CompletionsWithCtx(context.Background(), reviewRequest())
	if err == nil {
		t.Fatal("want an error when the session wrote no answer")
	}
	if !strings.Contains(err.Error(), "AGY_ERROR: quota reached") {
		t.Fatalf("error hides the CLI's own message: %v", err)
	}
}

func TestAgentCLIValidAnswerStandsDespiteANonZeroExit(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo '{"tool_calls":[{"name":"task_done","arguments":{}}]}' > "$result"; exit 1`))
	if _, err := client.CompletionsWithCtx(context.Background(), reviewRequest()); err != nil {
		t.Fatalf("a written answer should be used: %v", err)
	}
}

func TestAgentCLIBrokenAnswerIsAnError(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo 'not json' > "$result"`))
	if _, err := client.CompletionsWithCtx(context.Background(), reviewRequest()); err == nil {
		t.Fatal("want an error for an answer file that is not JSON")
	}
}

func TestAgentCLILeavesNoWorkFilesBehind(t *testing.T) {
	client := newFakeClient(t, fakeAgentCLI(t, `echo '{"tool_calls":[{"name":"task_done","arguments":{}}]}' > "$result"`))
	if _, err := client.CompletionsWithCtx(context.Background(), reviewRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentCLIWorkDir); !os.IsNotExist(err) {
		t.Fatalf("%s should be gone, stat: %v", agentCLIWorkDir, err)
	}
}

func TestAgentCLISubstitutesTheModelIntoTheCommand(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "model.txt")
	script := fakeAgentCLI(t, `echo "$2" > "`+seen+`"
echo '{"content":"ok"}' > "$result"`)
	client := newFakeClient(t, script+" {model}")
	req := reviewRequest()
	req.Tools = nil
	if _, err := client.CompletionsWithCtx(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "some-model" {
		t.Fatalf("got %q", raw)
	}
}

func TestAgentCLIRendersEarlierToolTurns(t *testing.T) {
	saved := filepath.Join(t.TempDir(), "prompt-copy.md")
	client := newFakeClient(t, fakeAgentCLI(t, `cp "$prompt" "`+saved+`"
echo '{"tool_calls":[{"name":"task_done","arguments":{}}]}' > "$result"`))
	req := reviewRequest()
	req.Messages = append(req.Messages,
		NewToolCallMessage("", []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "code_comment", Arguments: `{"comments":["EARLIER-ARGS"]}`}}}, NativeTurn{}, ""),
		NewToolResultMessage("c1", "EARLIER-RESULT"),
	)
	if _, err := client.CompletionsWithCtx(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"EARLIER-ARGS", "EARLIER-RESULT"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("prompt lost %q from the earlier turns", want)
		}
	}
}

func TestAgentCLIWithoutACommandIsAnError(t *testing.T) {
	t.Chdir(t.TempDir())
	client := NewAgentCLIClient(ClientConfig{Model: "some-model"})
	if _, err := client.CompletionsWithCtx(context.Background(), reviewRequest()); err == nil || !strings.Contains(err.Error(), envOCRAgentCLICommand) {
		t.Fatalf("want an error naming %s, got %v", envOCRAgentCLICommand, err)
	}
}

func TestNewLLMClientBuildsTheAgentCLIClient(t *testing.T) {
	ep := ResolvedEndpoint{Model: "m", Protocol: ProtocolAgentCLI, AgentCommand: "true"}
	if _, ok := NewLLMClient(ep, nil, nil).(*AgentCLIClient); !ok {
		t.Fatal("protocol agent-cli should build an AgentCLIClient")
	}
}

func TestProtocolAgentCLIIsAccepted(t *testing.T) {
	if got := NormalizeProtocol(" Agent-CLI "); got != ProtocolAgentCLI {
		t.Fatalf("got %q", got)
	}
	if err := ValidateProtocol(ProtocolAgentCLI); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAgentCLIFromEnvironmentNeedsNoURLOrToken(t *testing.T) {
	t.Setenv(envOCRLLMProtocol, "agent-cli")
	t.Setenv(envOCRAgentCLICommand, "agy --prompt={prompt}")
	t.Setenv(envOCRLLMModel, "gemini-x")
	t.Setenv(envOCRLLMURL, "")
	t.Setenv(envOCRLLMToken, "")
	ep, ok, err := tryOCREnv("")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ep.Protocol != ProtocolAgentCLI || ep.AgentCommand != "agy --prompt={prompt}" || ep.Model != "gemini-x" || !ep.AmbientAuth {
		t.Fatalf("got %+v", ep)
	}
}

func TestResolveAgentCLIFromEnvironmentRequiresACommand(t *testing.T) {
	t.Setenv(envOCRLLMProtocol, "agent-cli")
	t.Setenv(envOCRAgentCLICommand, "")
	t.Setenv(envOCRLLMModel, "gemini-x")
	if _, _, err := tryOCREnv(""); err == nil || !strings.Contains(err.Error(), envOCRAgentCLICommand) {
		t.Fatalf("want an error naming %s, got %v", envOCRAgentCLICommand, err)
	}
}
