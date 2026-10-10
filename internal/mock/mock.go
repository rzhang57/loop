// Package mock fakes the model and tools so the interface can be tried without signing in.
// Model implements llm.Model by replaying a fixed three-step script, streaming reasoning and text a
// word at a time and choosing the step from how many tool calls followed the last user message.
// Tool implements agent.Tool with a canned delay and result, Tools returns the set the script calls,
// and Models lists pretend models for the picker. Used by cmd/loop's --mock mode and by tests.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
)

// Model replays a fixed three-step conversation, for trying the interface without signing in.
type Model struct {
	WordDelay time.Duration
	calls     int
}

type step struct {
	reasoning []string
	text      string
	calls     [][2]string
}

var script = []step{
	{
		reasoning: []string{"**Getting oriented**\n\nThe user wants an overview, so I should list the files before reading anything."},
		text:      "I'll take a look around this project first.",
		calls:     [][2]string{{"list_files", `{"path":"."}`}},
	},
	{
		reasoning: []string{
			"**Choosing what to read**\n\nThere's a README and the entry point. The README explains the project best.",
			"**Checking the docs too**\n\nThe docs page might have more, so I'll fetch it at the same time.",
		},
		text: "Let me read the README and check the docs page.",
		calls: [][2]string{
			{"read_file", `{"path":"README.md"}`},
			{"fetch_page", `{"url":"https://example.com/docs"}`},
		},
	},
	{
		text: "Here's what I found:\n\n" +
			"- **README.md** explains what the project does and how to install it.\n" +
			"- The `cmd/` folder holds the entry point, and `internal/` holds the packages.\n" +
			"- The docs page couldn't be reached, so I skipped it.\n\n" +
			"The entry point is small:\n\n" +
			"```go\nfunc main() {\n\tif err := run(os.Args[1:]); err != nil {\n\t\tos.Exit(1)\n\t}\n}\n```\n\n" +
			"Want me to dig into any of these?",
	},
}

func (m *Model) Respond(ctx context.Context, req llm.Request, onDelta func(llm.Delta)) (llm.Response, error) {
	s := script[min(toolCallsSinceUser(req.Input), len(script)-1)]
	onDelta(llm.Delta{Kind: llm.ResponseStarted})
	for _, part := range s.reasoning {
		onDelta(llm.Delta{Kind: llm.ReasoningPart})
		if err := m.stream(ctx, part, llm.ReasoningText, onDelta); err != nil {
			return llm.Response{}, err
		}
	}
	if err := m.stream(ctx, s.text, llm.OutputText, onDelta); err != nil {
		return llm.Response{}, err
	}

	resp := llm.Response{Items: []llm.Item{llm.AssistantMessage(s.text)}}
	for _, c := range s.calls {
		m.calls++
		resp.Items = append(resp.Items, llm.Item{
			Type:      "function_call",
			CallID:    fmt.Sprintf("mock-%d", m.calls),
			Name:      c[0],
			Arguments: c[1],
		})
	}
	return resp, nil
}

// stream sends text a word at a time, pausing WordDelay between words like a real model.
func (m *Model) stream(ctx context.Context, text string, kind llm.DeltaKind, onDelta func(llm.Delta)) error {
	for i, word := range strings.SplitAfter(text, " ") {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.WordDelay):
			}
		}
		onDelta(llm.Delta{Kind: kind, Text: word})
	}
	return nil
}

func toolCallsSinceUser(input []llm.Item) int {
	calls := 0
	for i := len(input) - 1; i >= 0 && input[i].Role != "user"; i-- {
		if input[i].IsToolCall() {
			calls++
		}
	}
	return calls
}

// Tool pretends to do work for a while, then returns a canned result.
type Tool struct {
	Name     string
	Took     time.Duration
	Output   string
	Err      error
	Argument string
}

func (t Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Namespace:   llm.Namespace{Name: "local", Description: "Pretend tools."},
		Name:        t.Name,
		Description: "A pretend tool for trying out the interface.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

func (t Tool) Summary(args json.RawMessage) string {
	var a map[string]string
	json.Unmarshal(args, &a)
	return a[t.Argument]
}

func (t Tool) Run(ctx context.Context, _ json.RawMessage) (agent.Result, error) {
	select {
	case <-ctx.Done():
		return agent.Result{}, ctx.Err()
	case <-time.After(t.Took):
	}
	return agent.Result{Text: t.Output}, t.Err
}

// Tools returns the pretend tools the scripted conversation calls.
func Tools() []Tool {
	return []Tool{
		{Name: "list_files", Argument: "path", Took: 1200 * time.Millisecond,
			Output: "README.md\nMakefile\ncmd/\ninternal/\ngo.mod\ngo.sum"},
		{Name: "read_file", Argument: "path", Took: 900 * time.Millisecond,
			Output: "# loop\n\nloop is a clean base for building agent harnesses."},
		{Name: "fetch_page", Argument: "url", Took: 1600 * time.Millisecond,
			Err: errors.New("connection refused")},
	}
}

// Models lists pretend models for the picker.
type Models struct{}

func (Models) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{
		{Slug: "mock", DisplayName: "Mock"},
		{Slug: "mock-fast", DisplayName: "Mock Fast"},
	}, nil
}
