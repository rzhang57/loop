package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/mock"
)

func TestToolError(t *testing.T) {
	failed := func(output string) llm.Item { item := llm.ToolOutput("c", output); item.Failed = true; return item }
	for _, tc := range []struct {
		item          llm.Item
		text, errText string
		failed        bool
	}{
		{llm.ToolOutput("c", "all good"), "all good", "", false},
		{llm.ToolOutput("c", "build log\nerror: not really a failure"), "build log\nerror: not really a failure", "", false},
		{failed("partial output\nerror: exit status 2"), "partial output", "exit status 2", true},
		{failed("a\nerror: printed by the tool\nerror: last"), "a\nerror: printed by the tool", "last", true},
		{failed("unknown tool x"), "", "unknown tool x", true},
	} {
		text, errText, failed := agent.ToolError(tc.item)
		if text != tc.text || errText != tc.errText || failed != tc.failed {
			t.Errorf("ToolError(%+v) = %q, %q, %v; want %q, %q, %v", tc.item, text, errText, failed, tc.text, tc.errText, tc.failed)
		}
	}
}

func TestFailedCallsCanBeToldApartInTheHistory(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{call("c1", "echo", `"a"`), call("c2", "missing", `{}`), call("c3", "fetch_page", `{"url":"x"}`)}},
		{Items: []llm.Item{llm.AssistantMessage("done")}},
	}}
	failing := mock.Tool{Name: "fetch_page", Argument: "url", Output: "half", Err: errors.New("connection refused")}
	a := agent.New(model, &echoTool{}, failing)
	if err := a.Send(context.Background(), "m", "go", func(agent.Event) {}); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		text, errText string
		failed        bool
	}{
		"c1": {`echoed "a"`, "", false},
		"c2": {"", "unknown tool missing", true},
		"c3": {"half", "connection refused", true},
	}
	for _, item := range a.History() {
		if item.Type != "function_call_output" {
			continue
		}
		text, errText, failed := agent.ToolError(item)
		if w := want[item.CallID]; text != w.text || errText != w.errText || failed != w.failed {
			t.Errorf("%s: got %q, %q, %v; want %+v", item.CallID, text, errText, failed, w)
		}
	}
}

func TestSummaryDescribesPastCalls(t *testing.T) {
	a := agent.New(&scriptedModel{}, mock.Tool{Name: "read_file", Argument: "path"})
	if got := a.Summary("read_file", `{"path":"README.md"}`); got != "README.md" {
		t.Fatalf("got %q", got)
	}
	if got := a.Summary("gone", `{}`); got != "" {
		t.Fatalf("unknown tool summarized as %q", got)
	}
}
