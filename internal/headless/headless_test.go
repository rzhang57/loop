package headless

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/session"
	"github.com/rzhang57/loop/internal/store"
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

type echoTool struct{}

func (echoTool) Spec() llm.ToolSpec             { return llm.ToolSpec{Name: "bash"} }
func (echoTool) Summary(json.RawMessage) string { return "" }
func (echoTool) Run(context.Context, json.RawMessage) (agent.Result, error) {
	return agent.Result{Text: "README.md"}, nil
}

func newRunner(t *testing.T, model llm.Model) (Runner, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return Runner{Agent: agent.New(model, echoTool{}), Store: s, Now: time.Now}, s
}

func TestRunReportsEveryStep(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{llm.AssistantMessage("Looking."), {Type: "function_call", CallID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}}, Usage: llm.Usage{InputTokens: 100}},
		{Items: []llm.Item{llm.AssistantMessage("Done.")}, Usage: llm.Usage{InputTokens: 150, CachedTokens: 96, OutputTokens: 5}},
	}}
	r, _ := newRunner(t, model)

	report := r.Run(context.Background(), "", "gpt-5.6-luna", "do it")
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if report.Text != "Done." || report.Turns != 2 || report.Usage != (llm.Usage{InputTokens: 250, CachedTokens: 96, OutputTokens: 5}) {
		t.Fatalf("report %+v", report)
	}
	if len(report.Steps) != 3 || report.Steps[1].Tool != "bash" || report.Steps[1].Arguments != `{"command":"ls"}` || report.Steps[1].Output != "README.md" {
		t.Fatalf("steps %+v", report.Steps)
	}
}

func TestSessionsContinueAcrossRuns(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{llm.AssistantMessage("first")}},
		{Items: []llm.Item{llm.AssistantMessage("second")}},
	}}
	r, s := newRunner(t, model)
	first := r.Run(context.Background(), "", "gpt-5.6-luna", "one")

	again := Runner{Agent: agent.New(model, echoTool{}), Store: s, Now: time.Now}
	second := again.Run(context.Background(), first.Session, "", "two")
	if second.Error != "" || second.Model != "gpt-5.6-luna" {
		t.Fatalf("continuing should reuse the session's model: %+v", second)
	}
	if got := len(model.requests[1].Input); got != 3 {
		t.Fatalf("the second run sent %d items, want the first exchange plus the new message", got)
	}
}

func TestRunErrorsAreReported(t *testing.T) {
	r, _ := newRunner(t, &scriptedModel{})
	if report := r.Run(context.Background(), "nope", "m", "x"); report.Error != "no session nope" {
		t.Fatalf("got %q", report.Error)
	}
	if report := r.Run(context.Background(), "", "", "x"); report.Error == "" {
		t.Fatal("a run without a model should fail")
	}
}

type failingModel struct{}

func (failingModel) Respond(context.Context, llm.Request, func(llm.Delta)) (llm.Response, error) {
	return llm.Response{}, errors.New("usage limit reached")
}

func TestModelFailureIsReportedAndSessionKept(t *testing.T) {
	r, s := newRunner(t, failingModel{})
	r.Dir = "/work"
	saved := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return saved }
	report := r.Run(context.Background(), "", "m", "hi")
	if report.Error != "usage limit reached" || report.Steps == nil {
		t.Fatalf("report %+v", report)
	}
	sess, ok, _ := session.Load(context.Background(), s, report.Session)
	if !ok {
		t.Fatal("the session should still be saved so it can be continued")
	}
	if sess.Dir != "/work" || !sess.Updated.Equal(saved) {
		t.Fatalf("session saved with dir %q at %v, want /work at %v", sess.Dir, sess.Updated, saved)
	}
}

func TestExplicitModelOverridesTheSessions(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{llm.AssistantMessage("a")}},
		{Items: []llm.Item{llm.AssistantMessage("b")}},
	}}
	r, _ := newRunner(t, model)
	first := r.Run(context.Background(), "", "gpt-5.6-luna", "one")
	second := r.Run(context.Background(), first.Session, "gpt-5.6-terra", "two")
	if second.Model != "gpt-5.6-terra" || model.requests[1].Model != "gpt-5.6-terra" {
		t.Fatalf("explicit model lost: report %q, request %q", second.Model, model.requests[1].Model)
	}
}

type pieceModel struct{}

func (pieceModel) Respond(_ context.Context, _ llm.Request, onDelta func(llm.Delta)) (llm.Response, error) {
	onDelta(llm.Delta{Kind: llm.OutputText, Text: "Hel"})
	onDelta(llm.Delta{Kind: llm.OutputText, Text: "lo"})
	return llm.Response{Items: []llm.Item{llm.AssistantMessage("Hello")}}, nil
}

func TestStreamedPiecesBecomeOneStep(t *testing.T) {
	r, _ := newRunner(t, pieceModel{})
	report := r.Run(context.Background(), "", "m", "hi")
	if len(report.Steps) != 1 || report.Steps[0].Text != "Hello" || report.Text != "Hello" {
		t.Fatalf("steps %+v text %q", report.Steps, report.Text)
	}
}

type brokenTool struct{}

func (brokenTool) Spec() llm.ToolSpec             { return llm.ToolSpec{Name: "bash"} }
func (brokenTool) Summary(json.RawMessage) string { return "" }
func (brokenTool) Run(context.Context, json.RawMessage) (agent.Result, error) {
	time.Sleep(5 * time.Millisecond)
	return agent.Result{Text: "partial"}, errors.New("exit status 1")
}

func TestToolErrorsAreReported(t *testing.T) {
	model := &scriptedModel{responses: []llm.Response{
		{Items: []llm.Item{{Type: "function_call", CallID: "c1", Name: "bash", Arguments: "{}"}}},
		{Items: []llm.Item{llm.AssistantMessage("ok")}},
	}}
	r, _ := newRunner(t, model)
	r.Agent = agent.New(model, brokenTool{})
	step := r.Run(context.Background(), "", "m", "one").Steps[0]
	if step.Error != "exit status 1" || step.Output != "partial" || step.DurationMS < 5 {
		t.Fatalf("tool step %+v", step)
	}
}

type reasoningModel struct{}

func (reasoningModel) Respond(_ context.Context, _ llm.Request, onDelta func(llm.Delta)) (llm.Response, error) {
	for _, d := range []llm.Delta{
		{Kind: llm.ResponseStarted},
		{Kind: llm.ReasoningPart}, {Kind: llm.ReasoningText, Text: "**One**"},
		{Kind: llm.ReasoningPart}, {Kind: llm.ReasoningText, Text: "**Two**"},
		{Kind: llm.OutputText, Text: "done"},
	} {
		onDelta(d)
	}
	return llm.Response{Items: []llm.Item{llm.AssistantMessage("done")}}, nil
}

func TestReasoningBecomesOneStep(t *testing.T) {
	r, _ := newRunner(t, reasoningModel{})
	report := r.Run(context.Background(), "", "m", "hi")
	if len(report.Steps) != 2 || report.Steps[0].Type != "reasoning" || report.Steps[0].Text != "**One**\n\n**Two**" || report.Text != "done" {
		t.Fatalf("steps %+v text %q", report.Steps, report.Text)
	}
}

type deltasModel []llm.Delta

func (d deltasModel) Respond(_ context.Context, _ llm.Request, onDelta func(llm.Delta)) (llm.Response, error) {
	for _, delta := range d {
		onDelta(delta)
	}
	return llm.Response{}, nil
}

func TestReasoningNeverReachesTheFinalText(t *testing.T) {
	onlyReasoning := deltasModel{{Kind: llm.ReasoningText, Text: "**Thinking hard**"}}
	r, _ := newRunner(t, onlyReasoning)
	if report := r.Run(context.Background(), "", "m", "hi"); report.Text != "" {
		t.Fatalf("reasoning leaked into the answer: %q", report.Text)
	}

	interleaved := deltasModel{
		{Kind: llm.OutputText, Text: "first "},
		{Kind: llm.ReasoningText, Text: "**Aside**"},
		{Kind: llm.OutputText, Text: "second"},
	}
	r, _ = newRunner(t, interleaved)
	report := r.Run(context.Background(), "", "m", "hi")
	if report.Text != "first second" {
		t.Fatalf("text around late reasoning should stay together, got %q", report.Text)
	}
}
