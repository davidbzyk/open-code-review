// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agentCLIWorkDir holds each session's prompt and answer. It lives in the working
// directory because that is the one place every coding-agent sandbox can write.
const agentCLIWorkDir = ".ocr-agent"

// agentCLIOutputTail bounds how much of a failed CLI's output an error quotes.
const agentCLIOutputTail = 2000

// agentCLIWaitDelay is how long a cancelled CLI gets to close its pipes.
const agentCLIWaitDelay = 5 * time.Second

// agentCLIOwnTools are OCR's code-reading tools; a coding-agent CLI reads code with
// its own, so only the tools that report a result are described to it.
var agentCLIOwnTools = map[string]struct{}{
	"code_search":    {},
	"file_find":      {},
	"file_read":      {},
	"file_read_diff": {},
}

// AgentCLIClient runs each model call as one headless session of a coding-agent CLI,
// using that CLI's own login, and reads the answer from a file the session writes.
type AgentCLIClient struct {
	command string
	model   string
	timeout time.Duration
}

// NewAgentCLIClient builds a client that runs cfg.AgentCommand for every call.
func NewAgentCLIClient(cfg ClientConfig) *AgentCLIClient {
	return &AgentCLIClient{command: cfg.AgentCommand, model: cfg.Model, timeout: cfg.Timeout}
}

// agentCLIAnswer is the JSON document a session writes as its answer.
type agentCLIAnswer struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_calls"`
}

// CompletionsWithCtx runs one CLI session for req and returns what it reported.
func (c *AgentCLIClient) CompletionsWithCtx(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if strings.TrimSpace(c.command) == "" {
		return nil, fmt.Errorf("agent CLI: no command configured; set %s", envOCRAgentCLICommand)
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	dir, err := newAgentCLISessionDir()
	if err != nil {
		return nil, err
	}
	defer removeAgentCLISessionDir(dir)

	promptPath := filepath.Join(dir, "prompt.md")
	resultPath := filepath.Join(dir, "result.json")
	if err := os.WriteFile(promptPath, []byte(renderAgentCLIPrompt(req, resultPath)), 0o600); err != nil {
		return nil, fmt.Errorf("agent CLI: write prompt: %w", err)
	}

	instruction := "Read the file " + promptPath + " and do exactly what it says."
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, "bash", "-c", c.expandCommand(instruction, req.Model))
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.WaitDelay = agentCLIWaitDelay
	runErr := cmd.Run()

	answer, readErr := readAgentCLIAnswer(resultPath)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("agent CLI: session wrote no answer (%v): %s", runErr, tailOf(output.String()))
		}
		return nil, fmt.Errorf("agent CLI: %w: %s", readErr, tailOf(output.String()))
	}
	return agentCLIResponse(answer, req, c.model), nil
}

// expandCommand fills the command's placeholders, shell-quoting each value.
func (c *AgentCLIClient) expandCommand(instruction, model string) string {
	if model == "" {
		model = c.model
	}
	return strings.NewReplacer("{prompt}", shellQuote(instruction), "{model}", shellQuote(model)).Replace(c.command)
}

// shellQuote makes s one literal word in a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newAgentCLISessionDir creates this session's absolute working folder.
func newAgentCLISessionDir() (string, error) {
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("agent CLI: session id: %w", err)
	}
	dir, err := filepath.Abs(filepath.Join(agentCLIWorkDir, hex.EncodeToString(id)))
	if err != nil {
		return "", fmt.Errorf("agent CLI: session folder: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("agent CLI: session folder: %w", err)
	}
	return dir, nil
}

// removeAgentCLISessionDir deletes a session's folder, and the parent once empty.
func removeAgentCLISessionDir(dir string) {
	_ = os.RemoveAll(dir)
	_ = os.Remove(filepath.Dir(dir))
}

// readAgentCLIAnswer parses the answer file; os.ErrNotExist means none was written.
func readAgentCLIAnswer(path string) (*agentCLIAnswer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var answer agentCLIAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("answer file is not valid JSON: %w", err)
	}
	return &answer, nil
}

// agentCLIResponse turns a session's answer into the response OCR's loop expects.
func agentCLIResponse(answer *agentCLIAnswer, req ChatRequest, model string) *ChatResponse {
	calls := make([]ToolCall, 0, len(answer.ToolCalls)+1)
	finished := false
	for i, call := range answer.ToolCalls {
		args := string(call.Arguments)
		if args == "" {
			args = "{}"
		}
		finished = finished || call.Name == "task_done"
		calls = append(calls, agentCLIToolCall(i, call.Name, args))
	}
	// A session is one shot: when it ends the task is over, and without task_done
	// OCR's loop would start another full session just to be told so.
	if !finished && agentCLIOffersTool(req.Tools, "task_done") {
		calls = append(calls, agentCLIToolCall(len(calls), "task_done", `{"state":"DONE"}`))
	}
	message := ResponseMessage{Role: "assistant", ToolCalls: calls}
	if answer.Content != "" {
		content := answer.Content
		message.Content = &content
	}
	return &ChatResponse{Model: model, Choices: []Choice{{Message: message, FinishReason: "stop"}}}
}

// agentCLIToolCall builds the i'th tool call of a session's answer.
func agentCLIToolCall(i int, name, args string) ToolCall {
	return ToolCall{ID: fmt.Sprintf("call_%d", i+1), Type: "function", Function: FunctionCall{Name: name, Arguments: args}}
}

// agentCLIOffersTool reports whether the request offers the named tool.
func agentCLIOffersTool(tools []ToolDef, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

// renderAgentCLIPrompt writes the conversation and how to answer as one document.
func renderAgentCLIPrompt(req ChatRequest, resultPath string) string {
	var b strings.Builder
	for i := range req.Messages {
		renderAgentCLIMessage(&b, &req.Messages[i])
	}
	b.WriteString("## How to answer\n\n")
	b.WriteString("You are running inside a code review tool. The working directory is the repository at the reviewed commit. ")
	b.WriteString("Where the instructions above mention context tools, use your own tools to read and search the code. ")
	b.WriteString("Change no file except the answer file below.\n\n")
	b.WriteString("Write your answer to this file as one JSON object and nothing else:\n\n")
	b.WriteString("RESULT_FILE: " + resultPath + "\n\n")
	reports := agentCLIReportTools(req.Tools)
	if len(reports) == 0 {
		b.WriteString("Use this shape: {\"content\": \"<your reply>\"}\n")
		return b.String()
	}
	b.WriteString("Use this shape, with one entry for each tool you call, in order:\n\n")
	b.WriteString("{\"tool_calls\": [{\"name\": \"<tool name>\", \"arguments\": { ... }}]}\n\n")
	b.WriteString("The tools you can name:\n\n")
	for _, tool := range reports {
		schema, err := json.Marshal(tool.Function.Parameters)
		if err != nil {
			schema = []byte("{}")
		}
		b.WriteString("### " + tool.Function.Name + "\n\n" + tool.Function.Description + "\n\nArguments schema: " + string(schema) + "\n\n")
	}
	return b.String()
}

// renderAgentCLIMessage appends one conversation turn, tool calls included.
func renderAgentCLIMessage(b *strings.Builder, m *Message) {
	b.WriteString("## " + m.Role + "\n\n")
	if text := m.ExtractText(); text != "" {
		b.WriteString(text + "\n\n")
	}
	for _, call := range m.ToolCalls {
		b.WriteString("Called " + call.Function.Name + " with: " + call.Function.Arguments + "\n\n")
	}
}

// agentCLIReportTools drops the tools the CLI has its own version of.
func agentCLIReportTools(tools []ToolDef) []ToolDef {
	reports := make([]ToolDef, 0, len(tools))
	for _, tool := range tools {
		if _, own := agentCLIOwnTools[tool.Function.Name]; !own {
			reports = append(reports, tool)
		}
	}
	return reports
}

// tailOf keeps the end of a CLI's output, where its error usually is.
func tailOf(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > agentCLIOutputTail {
		return s[len(s)-agentCLIOutputTail:]
	}
	return s
}
