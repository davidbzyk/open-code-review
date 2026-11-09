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

// agentStepStop is what a test client's exit panics with.
type agentStepStop struct{ code int }

// newAgentStepTestClient builds a client whose exit panics, so a test can see the stop.
func newAgentStepTestClient(dir string) *AgentStepClient {
	client := NewAgentStepClient(ClientConfig{Model: "some-model", AgentStepDir: dir})
	client.exit = func(code int) { panic(agentStepStop{code}) }
	return client
}

// answerCall writes the answer an agent would leave for the n'th call.
func answerCall(t *testing.T, dir, name, answer string) {
	t.Helper()
	call := filepath.Join(dir, name)
	if err := os.MkdirAll(call, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(call, "result.json"), []byte(answer), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stopCode runs a call expected to stop the process and returns the exit code.
func stopCode(t *testing.T, call func()) (code int) {
	t.Helper()
	defer func() {
		stop, ok := recover().(agentStepStop)
		if !ok {
			t.Fatal("the call returned instead of stopping")
		}
		code = stop.code
	}()
	call()
	return 0
}

func TestAgentStepWritesTheJobAndStopsAtAnUnansweredCall(t *testing.T) {
	dir := t.TempDir()
	client := newAgentStepTestClient(dir)
	ctx := WithRequestMeta(context.Background(), RequestMeta{Model: "some-model", FilePath: "infra/main.tf", TaskType: "MAIN_TASK", RequestNo: 1})

	code := stopCode(t, func() { _, _ = client.CompletionsWithCtx(ctx, reviewRequest()) })

	if code != ExitAgentStepPending {
		t.Fatalf("exit code %d, want %d", code, ExitAgentStepPending)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "call-001", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "RESULT_FILE: " + filepath.Join(dir, "call-001", "result.json"); !strings.Contains(string(prompt), want) {
		t.Fatalf("prompt has no %q:\n%s", want, prompt)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "call-001", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]string
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["task"] != "MAIN_TASK" || meta["group"] != "infra/main.tf" {
		t.Fatalf("meta %v", meta)
	}
}

func TestAgentStepReplaysAnsweredCallsInOrder(t *testing.T) {
	dir := t.TempDir()
	answerCall(t, dir, "call-001", `{"content":"first"}`)
	answerCall(t, dir, "call-002", `{"content":"second"}`)
	client := newAgentStepTestClient(dir)
	req := ChatRequest{Messages: []Message{NewTextMessage("user", "hello")}}

	for _, want := range []string{"first", "second"} {
		resp, err := client.CompletionsWithCtx(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Content() != want {
			t.Fatalf("got %q, want %q", resp.Content(), want)
		}
	}
	stopCode(t, func() { _, _ = client.CompletionsWithCtx(context.Background(), req) })
	if _, err := os.Stat(filepath.Join(dir, "call-003", "prompt.md")); err != nil {
		t.Fatalf("the first unanswered call should get the job: %v", err)
	}
}

func TestAgentStepReportsASessionThatWroteNoAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "call-001"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "call-001", "prompt.md"), []byte("job"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := newAgentStepTestClient(dir)

	_, err := client.CompletionsWithCtx(context.Background(), reviewRequest())

	if err == nil || !strings.Contains(err.Error(), "wrote no answer") {
		t.Fatalf("want an error naming the missing answer, got %v", err)
	}
}

func TestAgentStepReportsAnAnswerThatIsNotJSON(t *testing.T) {
	dir := t.TempDir()
	answerCall(t, dir, "call-001", "not json")
	client := newAgentStepTestClient(dir)

	if _, err := client.CompletionsWithCtx(context.Background(), reviewRequest()); err == nil {
		t.Fatal("want an error for an answer that is not JSON")
	}
}

func TestAgentStepNeedsADirectory(t *testing.T) {
	client := newAgentStepTestClient(" ")

	_, err := client.CompletionsWithCtx(context.Background(), reviewRequest())

	if err == nil || !strings.Contains(err.Error(), envOCRAgentStepDir) {
		t.Fatalf("want an error naming %s, got %v", envOCRAgentStepDir, err)
	}
}

func TestNewLLMClientBuildsTheAgentStepClient(t *testing.T) {
	ep := ResolvedEndpoint{Model: "m", Protocol: ProtocolAgentStep, AgentStepDir: "/tmp/steps"}
	if _, ok := NewLLMClient(ep, nil, nil).(*AgentStepClient); !ok {
		t.Fatal("protocol agent-step should build an AgentStepClient")
	}
}

func TestProtocolAgentStepIsAccepted(t *testing.T) {
	if got := NormalizeProtocol(" Agent-Step "); got != ProtocolAgentStep {
		t.Fatalf("got %q", got)
	}
	if err := ValidateProtocol(ProtocolAgentStep); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAgentStepFromEnvironmentNeedsNoURLOrToken(t *testing.T) {
	t.Setenv(envOCRLLMProtocol, "agent-step")
	t.Setenv(envOCRAgentStepDir, "/workspace/.ocr-steps")
	t.Setenv(envOCRLLMModel, "harbor-step")
	t.Setenv(envOCRLLMURL, "")
	t.Setenv(envOCRLLMToken, "")

	ep, ok, err := tryOCREnv("")

	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ep.Protocol != ProtocolAgentStep || ep.AgentStepDir != "/workspace/.ocr-steps" || ep.Model != "harbor-step" {
		t.Fatalf("got %+v", ep)
	}
}

func TestResolveAgentStepFromEnvironmentRequiresADirectory(t *testing.T) {
	t.Setenv(envOCRLLMProtocol, "agent-step")
	t.Setenv(envOCRAgentStepDir, "")
	t.Setenv(envOCRLLMModel, "harbor-step")

	if _, _, err := tryOCREnv(""); err == nil || !strings.Contains(err.Error(), envOCRAgentStepDir) {
		t.Fatalf("want an error naming %s, got %v", envOCRAgentStepDir, err)
	}
}
