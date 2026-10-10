package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/mock"
)

type scriptedModel struct {
	responses []llm.Response
	requests  []llm.Request
}

func (m *scriptedModel) Respond(_ context.Context, req llm.Request, onDelta func(llm.Delta)) (llm.Response, error) {
	m.requests = append(m.requests, req)
	resp := m.responses[len(m.requests)-1]
	for _, item := range resp.Items {
		if item.Type == "message" {
			onDelta(llm.Delta{Kind: llm.OutputText, Text: item.Content})
		}
	}
	return resp, nil
}

type echoTool struct{ ran int }

func (t *echoTool) Spec() llm.ToolSpec { return llm.ToolSpec{Name: "echo"} }

func (t *echoTool) Summary(args json.RawMessage) string { return string(args) }

func (t *echoTool) Run(_ context.Context, args json.RawMessage) (agent.Result, error) {
	t.ran++
	return agent.Result{Text: "echoed " + string(args)}, nil
}

func call(id, name, args string) llm.Item {
	return llm.Item{Type: "function_call", CallID: id, Name: name, Arguments: args}
}

func TestSendRunsToolsUntilFinalAnswer(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{llm.AssistantMessage("checking"), call("c1", "echo", `"a"`), call("c2", "missing", `{}`)}},
		{Items: []llm.Item{llm.AssistantMessage("done")}},
	}}
	tool := &echoTool{}
	a := agent.New(model, tool)

	var events []agent.Event
	if err := a.Send(context.Background(), "m", "go", func(e agent.Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}

	if tool.ran != 1 || len(model.requests) != 2 {
		t.Fatalf("tool ran %d times over %d requests", tool.ran, len(model.requests))
	}
	second := model.requests[1].Input
	outputs := map[string]string{}
	for _, item := range second {
		if item.Type == "function_call_output" {
			outputs[item.CallID] = item.Output
		}
	}
	if outputs["c1"] != `echoed "a"` || !strings.Contains(outputs["c2"], "unknown tool") {
		t.Fatalf("tool outputs sent back: %v", outputs)
	}

	var finished []agent.ToolFinished
	for _, e := range events {
		if f, ok := e.(agent.ToolFinished); ok {
			finished = append(finished, f)
		}
	}
	if len(finished) != 2 || finished[0].Err != nil || finished[1].Err == nil {
		t.Fatalf("finished events %+v", finished)
	}
}

func TestCancelledToolsStillGetOutputs(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{call("c1", "echo", `1`), call("c2", "echo", `2`)}},
	}}
	tool := &echoTool{}
	a := agent.New(model, tool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.Send(ctx, "m", "go", func(agent.Event) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if tool.ran != 0 {
		t.Fatal("tools ran after cancellation")
	}

	model.responses = append(model.responses, llm.Response{Items: []llm.Item{llm.AssistantMessage("ok")}})
	if err := a.Send(context.Background(), "m", "again", func(agent.Event) {}); err != nil {
		t.Fatal(err)
	}
	outputs := 0
	for _, item := range model.requests[1].Input {
		if item.Type == "function_call_output" {
			outputs++
		}
	}
	if outputs != 2 {
		t.Fatalf("history has %d tool outputs for 2 calls", outputs)
	}
}

func TestMockConversationRunsToCompletion(t *testing.T) {
	tools := mock.Tools()
	for i := range tools {
		tools[i].Took = time.Millisecond
	}
	agentTools := make([]agent.Tool, len(tools))
	for i, tool := range tools {
		agentTools[i] = tool
	}
	a := agent.New(&mock.Model{}, agentTools...)

	var text strings.Builder
	var started, failed int
	err := a.Send(context.Background(), "m", "look around", func(e agent.Event) {
		switch e := e.(type) {
		case agent.TextDelta:
			text.WriteString(e.Text)
		case agent.ToolStarted:
			started++
		case agent.ToolFinished:
			if e.Err != nil {
				failed++
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if started != 3 || failed != 1 || !strings.Contains(text.String(), "Want me to dig") {
		t.Fatalf("started=%d failed=%d text=%q", started, failed, text.String())
	}
}

type loopingModel struct{ calls int }

func (m *loopingModel) Respond(context.Context, llm.Request, func(llm.Delta)) (llm.Response, error) {
	m.calls++
	return llm.Response{Items: []llm.Item{call(fmt.Sprint(m.calls), "echo", `1`)}}, nil
}

func TestSendGivesUpAfterTooManyTurns(t *testing.T) {
	model := &loopingModel{}
	err := agent.New(model, &echoTool{}).Send(context.Background(), "m", "go", func(agent.Event) {})
	if err == nil || !strings.Contains(err.Error(), "50 model turns") || model.calls != 50 {
		t.Fatalf("got %v after %d calls", err, model.calls)
	}
}

type failingTool struct{}

func (failingTool) Spec() llm.ToolSpec             { return llm.ToolSpec{Name: "fail"} }
func (failingTool) Summary(json.RawMessage) string { return "" }
func (failingTool) Run(context.Context, json.RawMessage) (agent.Result, error) {
	return agent.Result{Text: "partial output"}, errors.New("exit status 2")
}

func TestFailedToolReportsErrorToModel(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{call("c1", "fail", `{}`)}},
		{Items: []llm.Item{llm.AssistantMessage("ok")}},
	}}
	if err := agent.New(model, failingTool{}).Send(context.Background(), "m", "go", func(agent.Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, item := range model.requests[1].Input {
		if item.Type == "function_call_output" && item.Output != "partial output\nerror: exit status 2" {
			t.Fatalf("model saw %q", item.Output)
		}
	}
}

type blockingTool struct{ started chan struct{} }

func (b blockingTool) Spec() llm.ToolSpec             { return llm.ToolSpec{Name: "block"} }
func (b blockingTool) Summary(json.RawMessage) string { return "" }
func (b blockingTool) Run(ctx context.Context, _ json.RawMessage) (agent.Result, error) {
	close(b.started)
	<-ctx.Done()
	return agent.Result{}, ctx.Err()
}

func TestCancellingDuringToolStopsTheTurn(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{call("c1", "block", `{}`), call("c2", "block", `{}`)}},
	}}
	tool := blockingTool{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-tool.started
		cancel()
	}()

	err := agent.New(model, tool).Send(ctx, "m", "go", func(agent.Event) {})
	if !errors.Is(err, context.Canceled) || len(model.requests) != 1 {
		t.Fatalf("got %v after %d requests", err, len(model.requests))
	}
}

func TestToolOutputReachesTheModelAndUsageIsReported(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{call("c1", "echo", `"a"`)}, Usage: llm.Usage{InputTokens: 10, CachedTokens: 4, OutputTokens: 1}},
		{Items: []llm.Item{llm.AssistantMessage("ok")}, Usage: llm.Usage{InputTokens: 20, CachedTokens: 8}},
	}}
	var total llm.Usage
	var finished agent.ToolFinished
	err := agent.New(model, &echoTool{}).Send(context.Background(), "m", "go", func(e agent.Event) {
		switch e := e.(type) {
		case agent.UsageReported:
			total = total.Add(e.Usage)
		case agent.ToolFinished:
			finished = e
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != (llm.Usage{InputTokens: 30, CachedTokens: 12, OutputTokens: 1}) {
		t.Fatalf("usage %+v", total)
	}
	if finished.Output != `echoed "a"` {
		t.Fatal("the UI should see the output too")
	}
	for _, item := range model.requests[1].Input {
		if item.Type == "function_call_output" && item.Output != `echoed "a"` {
			t.Fatalf("model got %+v", item)
		}
	}
}

func TestRestoredHistoryContinuesTheConversation(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{llm.AssistantMessage("first answer")}},
		{Items: []llm.Item{llm.AssistantMessage("second answer")}},
	}}
	first := agent.New(model)
	first.Send(context.Background(), "m", "one", func(agent.Event) {})

	second := agent.New(model)
	second.Restore(first.History())
	second.Send(context.Background(), "m", "two", func(agent.Event) {})
	if got := len(model.requests[1].Input); got != 3 {
		t.Fatalf("restored conversation sent %d items, want 3", got)
	}
}

type deltaModel struct{}

func (deltaModel) Respond(_ context.Context, _ llm.Request, onDelta func(llm.Delta)) (llm.Response, error) {
	onDelta(llm.Delta{Kind: llm.ResponseStarted})
	onDelta(llm.Delta{Kind: llm.ReasoningPart})
	onDelta(llm.Delta{Kind: llm.ReasoningText, Text: "**Plan**"})
	onDelta(llm.Delta{Kind: llm.OutputText, Text: "ok"})
	return llm.Response{Items: []llm.Item{llm.AssistantMessage("ok")}}, nil
}

func TestStreamPiecesBecomeEventsInOrder(t *testing.T) {
	var got []agent.Event
	if err := agent.New(deltaModel{}).Send(context.Background(), "m", "go", func(e agent.Event) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	want := []agent.Event{
		agent.RequestSent{}, agent.ResponseStarted{}, agent.ReasoningDelta{NewPart: true},
		agent.ReasoningDelta{Text: "**Plan**"}, agent.TextDelta{Text: "ok"}, agent.UsageReported{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events %#v", got)
	}
}
