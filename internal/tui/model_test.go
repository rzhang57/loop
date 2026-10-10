package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/auth"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/mock"
	"github.com/rzhang57/loop/internal/store"
)

func newMockModel(t *testing.T) *Model {
	t.Helper()
	var tools []agent.Tool
	for _, tool := range mock.Tools() {
		tool.Took = 5 * time.Millisecond
		tools = append(tools, tool)
	}
	return start(New(Deps{Agent: agent.New(&mock.Model{}, tools...), Models: mock.Models{}, Dir: t.TempDir()}))
}

func start(m *Model) *Model {
	styles := m.input.Styles()
	styles.Cursor.Blink = false
	m.input.SetStyles(styles)
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

// drain runs commands and feeds their messages back in until the model goes quiet,
// dropping timers like cursor blinks and animation frames that would never end.
func drain(m *Model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		switch msg := runBriefly(next).(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case streamed, modelsMsg, loginDoneMsg, logoutDoneMsg, sessionsMsg, sessionLoadedMsg, errorMsg:
			_, followUp := m.Update(msg)
			queue = append(queue, followUp)
		}
	}
}

func runBriefly(cmd tea.Cmd) tea.Msg {
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func press(m *Model, keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		_, cmd := m.Update(k)
		drain(m, cmd)
	}
}

func typeText(m *Model, text string) {
	for _, r := range text {
		press(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
)

func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

func TestConversationShowsTextAndOneLineToolCalls(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "look around")
	press(m, enter)

	out := screen(m)
	for _, want := range []string{"› look around", "✓ list_files .", "✓ read_file README.md", "✗ fetch_page https://example.com/docs · connection refused", "Want me to dig into any of these?"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen is missing %q\n%s", want, out)
		}
	}
	if m.running {
		t.Fatal("still running after the turn finished")
	}
	if strings.Contains(out, "go.sum") {
		t.Fatal("tool output should stay collapsed until expanded")
	}
}

func rowOf(m *Model, text string) int {
	for i, line := range strings.Split(screen(m), "\n") {
		if strings.Contains(line, text) {
			return i
		}
	}
	return -1
}

func click(m *Model, y int) {
	m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 5, Y: y, Button: tea.MouseLeft})
}

func drag(m *Model, from, to cell) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: from.x, Y: from.y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: to.x, Y: to.y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: to.x, Y: to.y, Button: tea.MouseLeft})
	return cmd
}

func openAll(m *Model) {
	for i := range m.entries {
		m.entries[i].expanded = true
	}
}

func TestClickingAStepOpensOnlyThatStep(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "look around")
	press(m, enter)

	click(m, rowOf(m, "list_files"))
	if out := screen(m); !strings.Contains(out, "│ go.sum") || strings.Contains(out, "│ # loop") {
		t.Fatalf("clicking should open only that tool call\n%s", out)
	}
	click(m, rowOf(m, "fetch_page"))
	if !strings.Contains(screen(m), "│ error: connection refused") {
		t.Fatalf("clicking another step should open it too\n%s", screen(m))
	}
	click(m, rowOf(m, "go.sum"))
	if strings.Contains(screen(m), "go.sum") {
		t.Fatal("clicking inside an open step should close it")
	}
	before := screen(m)
	click(m, rowOf(m, "Want me to dig"))
	if screen(m) != before {
		t.Fatal("clicking text that isn't a step should do nothing")
	}
}

func TestWheelScrollsTheTranscript(t *testing.T) {
	m := newMockModel(t)
	for range 4 {
		typeText(m, "look around")
		press(m, enter)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.scroll != 3 {
		t.Fatalf("wheel up should scroll back, scroll=%d", m.scroll)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.scroll != 0 {
		t.Fatalf("wheel down should scroll forward, scroll=%d", m.scroll)
	}
}

func TestSlashCommandsAutocompleteAndPickModel(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "/mo")
	if out := screen(m); !strings.Contains(out, "/model") || strings.Contains(out, "/logout") {
		t.Fatalf("suggestions should narrow to /model\n%s", out)
	}

	press(m, enter)
	if m.picker == nil || !strings.Contains(screen(m), "Choose a model") {
		t.Fatal("/model should open the picker")
	}
	press(m, down, enter)
	if m.modelName != "mock-fast" || m.picker != nil {
		t.Fatalf("picked %q", m.modelName)
	}
}

func TestEscapeStopsARunningTurn(t *testing.T) {
	m := newMockModel(t)
	m.deps.Agent = agent.New(&mock.Model{WordDelay: time.Second})
	typeText(m, "hello")
	_, cmd := m.Update(enter)
	if !m.running {
		t.Fatal("turn should be running")
	}
	m.Update(esc)
	drain(m, cmd)

	if m.running || !strings.Contains(screen(m), "Stopped.") {
		t.Fatalf("escape should stop the turn\n%s", screen(m))
	}
}

func TestSendingRequiresSignIn(t *testing.T) {
	s := openStore(t)
	m := newSignedOutModel(t, s, mock.Models{})

	typeText(m, "hi")
	press(m, enter)
	if m.running || !strings.Contains(screen(m), "not signed in, run /login first") {
		t.Fatalf("expected a sign-in prompt\n%s", screen(m))
	}
	if m.input.Value() != "hi" {
		t.Fatal("the unsent message should stay in the input")
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newSignedOutModel(t *testing.T, s *store.Store, models ModelLister) *Model {
	t.Helper()
	tools := mock.Tools()
	for i := range tools {
		tools[i].Took = time.Millisecond
	}
	return start(New(Deps{Agent: agent.New(&mock.Model{}, tools[0]), Auth: auth.New(s, auth.DefaultConfig()), Models: models, Store: s, Dir: t.TempDir()}))
}

type listedModels []llm.ModelInfo

func (l listedModels) ListModels(context.Context) ([]llm.ModelInfo, error) { return l, nil }

func TestSignInPicksPreferredModelAndRemembersIt(t *testing.T) {
	s := openStore(t)
	m := newSignedOutModel(t, s, listedModels{{Slug: "gpt-other", DisplayName: "Other"}, {Slug: preferredModel, DisplayName: "Sol"}})
	_, cmd := m.Update(loginDoneMsg{creds: auth.Credentials{Email: "me@example.com"}})
	drain(m, cmd)
	if m.email != "me@example.com" || m.modelName != preferredModel {
		t.Fatalf("after sign-in: email=%q model=%q", m.email, m.modelName)
	}
	if saved, _, _ := s.Get(context.Background(), keyModel); saved != preferredModel {
		t.Fatalf("model choice saved as %q", saved)
	}

	reopened := newSignedOutModel(t, s, listedModels{})
	if reopened.modelName != preferredModel {
		t.Fatalf("a new session should remember the model, got %q", reopened.modelName)
	}
}

func TestPendingMessageIsSentOnceAModelIsChosen(t *testing.T) {
	m := newMockModel(t)
	m.modelName = ""
	typeText(m, "hello")
	press(m, enter)
	if m.modelName != "mock" || !strings.Contains(screen(m), "Want me to dig into any of these?") {
		t.Fatalf("the first message should go out after picking a model\n%s", screen(m))
	}
}

func TestEndedSessionShowsSignedOut(t *testing.T) {
	m := newSignedOutModel(t, openStore(t), listedModels{})
	m.email = "me@example.com"
	m.Update(turnDoneMsg{err: auth.ErrSessionExpired})
	if m.email != "" || !strings.Contains(screen(m), "not signed in") {
		t.Fatalf("status should show signed out\n%s", screen(m))
	}
}

func TestClearHelpAndUnknownCommands(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "look around")
	press(m, enter)
	typeText(m, "/clear")
	press(m, enter)
	if len(m.entries) != 0 || strings.Contains(screen(m), "look around") {
		t.Fatal("/clear should empty the transcript")
	}
	recorder := &recordingModel{}
	m.deps.Agent = agent.New(recorder)
	typeText(m, "one")
	press(m, enter)
	typeText(m, "/clear")
	press(m, enter)
	typeText(m, "two")
	press(m, enter)
	if got := len(recorder.inputs[len(recorder.inputs)-1]); got != 1 {
		t.Fatalf("after /clear the model saw %d earlier items, want only the new message", got-1)
	}

	typeText(m, "/help")
	press(m, enter)
	if !strings.Contains(screen(m), "shift+enter new line") {
		t.Fatalf("/help should list keys\n%s", screen(m))
	}

	typeText(m, "/bogus now")
	press(m, enter)
	if !strings.Contains(screen(m), "unknown command /bogus now") {
		t.Fatalf("unknown commands should be reported\n%s", screen(m))
	}
}

func TestPickerEscapeKeepsModel(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "/model")
	press(m, enter, down, esc)
	if m.picker != nil || m.modelName != "mock" {
		t.Fatalf("esc should close the picker without changing the model, got %q", m.modelName)
	}
}

func TestInputIsIgnoredWhileRunningAndCtrlCStops(t *testing.T) {
	m := newMockModel(t)
	m.deps.Agent = agent.New(&mock.Model{WordDelay: time.Second})
	typeText(m, "first")
	_, cmd := m.Update(enter)
	typeText(m, "second")
	m.Update(enter)
	if users := slices.IndexFunc(m.entries, func(e entry) bool { return e.text == "second" }); users >= 0 {
		t.Fatal("a second message must not start while a turn runs")
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	drain(m, cmd)
	if m.running || !strings.Contains(screen(m), "Stopped.") {
		t.Fatal("ctrl+c should stop the running turn rather than quit")
	}
}

func TestScrollingMovesThroughLongTranscripts(t *testing.T) {
	m := newMockModel(t)
	for range 4 {
		typeText(m, "look around")
		press(m, enter)
	}
	bottom := screen(m)
	press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.scroll == 0 || screen(m) == bottom {
		t.Fatal("pgup should scroll back")
	}
	for range 50 {
		press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	top := m.scroll
	press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.scroll != top || !strings.Contains(screen(m), "loop") {
		t.Fatal("scrolling should stop at the top")
	}
	for range 60 {
		press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if m.scroll != 0 || screen(m) != bottom {
		t.Fatal("pgdown should return to the bottom")
	}
}

func TestNarrowWindowNeverOverflows(t *testing.T) {
	m := newMockModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	typeText(m, "look around")
	press(m, enter)
	openAll(m)
	typeText(m, "/")
	for _, line := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d wide in a 40 column window: %q", w, ansi.Strip(line))
		}
	}
}

func TestArrowKeysScrollWhenInputIsEmpty(t *testing.T) {
	m := newMockModel(t)
	for range 4 {
		typeText(m, "look around")
		press(m, enter)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.scroll != 3 {
		t.Fatalf("up should scroll by 3, scroll=%d", m.scroll)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.scroll != 0 {
		t.Fatalf("down should scroll back, scroll=%d", m.scroll)
	}
	typeText(m, "draft")
	press(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.scroll != 0 {
		t.Fatal("with text in the input, arrows belong to the input")
	}
}

type countingModels struct {
	listedModels
	calls int
}

func (c *countingModels) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	c.calls++
	return c.listedModels.ListModels(ctx)
}

func TestModelPickerOpensFromCachedList(t *testing.T) {
	s := openStore(t)
	lister := &countingModels{listedModels: listedModels{{Slug: preferredModel, DisplayName: "Sol"}, {Slug: "other", DisplayName: "Other"}}}
	m := newSignedOutModel(t, s, lister)
	_, cmd := m.Update(loginDoneMsg{creds: auth.Credentials{Email: "me@example.com"}})
	drain(m, cmd)

	for range 3 {
		typeText(m, "/model")
		press(m, enter)
		if m.picker == nil {
			t.Fatal("/model should open the picker")
		}
		press(m, esc)
	}
	if lister.calls != 1 {
		t.Fatalf("model list fetched %d times, want once", lister.calls)
	}
}

func TestSecondEnterWhileWaitingForModelsIsIgnored(t *testing.T) {
	m := newMockModel(t)
	m.modelName = ""
	typeText(m, "first")
	_, cmd := m.Update(enter)
	typeText(m, "second")
	m.Update(enter)
	drain(m, cmd)
	if slices.IndexFunc(m.entries, func(e entry) bool { return e.text == "second" }) >= 0 {
		t.Fatal("a second message must wait until the first is sent")
	}
	if m.input.Value() != "second" {
		t.Fatal("the unsent message should stay in the input")
	}
}

type recordingModel struct{ inputs [][]llm.Item }

func (r *recordingModel) Respond(_ context.Context, req llm.Request, _ func(llm.Delta)) (llm.Response, error) {
	r.inputs = append(r.inputs, req.Input)
	return llm.Response{}, nil
}

func TestEscapeCancelsAnOpenSignIn(t *testing.T) {
	m := newSignedOutModel(t, openStore(t), listedModels{})
	cancelled := false
	m.cancelLogin = func() { cancelled = true }

	typeText(m, "/login")
	press(m, enter)
	if !strings.Contains(screen(m), "already open") {
		t.Fatal("a second /login should be refused while one is open")
	}
	press(m, esc)
	m.Update(loginDoneMsg{err: context.Canceled})
	if !cancelled || m.cancelLogin != nil || !strings.Contains(screen(m), "Sign-in cancelled.") {
		t.Fatalf("esc should cancel the sign-in\n%s", screen(m))
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func TestLiveLineShowsWhatTheModelIsDoing(t *testing.T) {
	m := newMockModel(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	m.now = clock.Now
	m.running = true
	event := func(e agent.Event) { m.Update(agentEventMsg{event: e}) }

	event(agent.RequestSent{})
	if !strings.Contains(screen(m), "Waiting for the model · 0s") {
		t.Fatalf("before the model responds the live line should say so\n%s", screen(m))
	}
	event(agent.ResponseStarted{})
	clock.advance(3 * time.Second)
	if !strings.Contains(screen(m), "Thinking · 3s") {
		t.Fatalf("the timer should count up so a stuck request is visible\n%s", screen(m))
	}
	event(agent.ReasoningDelta{NewPart: true})
	event(agent.ReasoningDelta{Text: "**Reading the config**\n\nI will look at the config file."})
	if out := screen(m); !strings.Contains(out, "Reading the config · 3s") || strings.Contains(out, "config file") {
		t.Fatalf("the live line should show the latest heading only\n%s", out)
	}

	clock.advance(2 * time.Second)
	event(agent.ToolStarted{CallID: "c1", Name: "bash", Summary: "cat config.toml"})
	event(agent.ToolFinished{CallID: "c1", Output: "ok"})
	if out := screen(m); strings.Contains(out, "Thought for") || strings.Contains(out, "Reading the config") {
		t.Fatalf("once the tool starts there should be no separate thinking line\n%s", out)
	}
	click(m, rowOf(m, "Ran cat config.toml"))
	out := screen(m)
	thought, reasoning, command := strings.Index(out, "│ Thought for 5s"), strings.Index(out, "│ I will look at the config file."), strings.Index(out, "│ $ cat config.toml")
	if thought < 0 || reasoning < thought || command < reasoning {
		t.Fatalf("opening the call should show the reasoning that led to it, then the command\n%s", out)
	}
}

func TestThinkingWithoutReasoningLeavesNoTrace(t *testing.T) {
	m := newMockModel(t)
	m.Update(agentEventMsg{event: agent.RequestSent{}})
	m.Update(agentEventMsg{event: agent.TextDelta{Text: "hi"}})
	if slices.ContainsFunc(m.entries, func(e entry) bool { return e.kind == thinkingEntry }) {
		t.Fatal("a thinking step with nothing to show should disappear once the answer starts")
	}
}

func TestMockConversationFoldsReasoningIntoToolCalls(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "look around")
	press(m, enter)
	if out := screen(m); strings.Contains(out, "Thought for") || strings.Contains(out, "Getting oriented") {
		t.Fatalf("reasoning should be tucked inside the tool calls\n%s", out)
	}
	click(m, rowOf(m, "list_files"))
	if !strings.Contains(screen(m), "│ Getting oriented") {
		t.Fatalf("opening the first call should show the reasoning behind it\n%s", screen(m))
	}
}

func thinkingSteps(m *Model) int {
	n := 0
	for _, e := range m.entries {
		if e.kind == thinkingEntry {
			n++
		}
	}
	return n
}

func TestStoppingOrFailingMidThoughtClosesTheStep(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reasoning string
		err       error
		wantSteps int
		wantText  string
	}{
		{"cancel without reasoning", "", context.Canceled, 0, "Stopped."},
		{"cancel with reasoning", "**Reading the config**", context.Canceled, 1, "Thought for 2s · Reading the config"},
		{"error with reasoning", "**Reading the config**", errors.New("usage limit"), 1, "usage limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockModel(t)
			clock := &fakeClock{now: time.Unix(1000, 0)}
			m.now = clock.Now
			m.running = true
			m.Update(agentEventMsg{event: agent.RequestSent{}})
			if tc.reasoning != "" {
				m.Update(agentEventMsg{event: agent.ReasoningDelta{Text: tc.reasoning}})
			}
			clock.advance(2 * time.Second)
			m.Update(turnDoneMsg{err: tc.err})
			clock.advance(time.Minute)

			out := screen(m)
			if thinkingSteps(m) != tc.wantSteps || m.thinking != nil || !strings.Contains(out, tc.wantText) {
				t.Fatalf("steps=%d active=%v\n%s", thinkingSteps(m), m.thinking != nil, out)
			}
			if strings.Contains(out, "Waiting for the model") || strings.Contains(out, "1m") || strings.Contains(out, "62s") {
				t.Fatalf("a closed step must stop its timer\n%s", out)
			}
		})
	}
}

func TestThoughtSurvivesNoticesArrivingMidway(t *testing.T) {
	m := newMockModel(t)
	m.Update(agentEventMsg{event: agent.RequestSent{}})
	m.Update(loginURLMsg{url: "https://example.com/login"})
	m.Update(agentEventMsg{event: agent.ReasoningDelta{Text: "**Still here**"}})
	m.Update(agentEventMsg{event: agent.ToolStarted{CallID: "c1", Name: "bash", Summary: "ls"}})
	tool := m.entries[len(m.entries)-1].tool
	if m.thinking != nil || tool.thought == nil || tool.thought.summary != "**Still here**" {
		t.Fatal("the reasoning should reach the tool call despite the notice")
	}
}

func TestEachCallGetsItsOwnReasoningAndTheAnswerKeepsItsOwn(t *testing.T) {
	m := newMockModel(t)
	event := func(e agent.Event) { m.Update(agentEventMsg{event: e}) }
	event(agent.RequestSent{})
	event(agent.ReasoningDelta{Text: "**First**"})
	event(agent.ToolStarted{CallID: "c1", Name: "bash", Summary: "ls"})
	event(agent.ToolFinished{CallID: "c1"})
	event(agent.RequestSent{})
	event(agent.ReasoningDelta{Text: "**Second**"})
	event(agent.TextDelta{Text: "I'll check where we are."})
	event(agent.ReasoningDelta{Text: "\n\n**Late**"})
	event(agent.ToolStarted{CallID: "c2", Name: "bash", Summary: "pwd"})
	event(agent.ToolFinished{CallID: "c2"})
	event(agent.RequestSent{})
	event(agent.ReasoningDelta{Text: "**Answering**"})
	event(agent.TextDelta{Text: "done"})
	m.Update(turnDoneMsg{})

	var thoughts []string
	for _, e := range m.entries {
		if e.tool != nil && e.tool.thought != nil {
			thoughts = append(thoughts, e.tool.thought.summary)
		}
	}
	if !reflect.DeepEqual(thoughts, []string{"**First**", "**Second**\n\n**Late**"}) {
		t.Fatalf("each call should carry the reasoning of its own request, got %q", thoughts)
	}
	last := len(m.entries) - 1
	if thinkingSteps(m) != 1 || m.entries[last-1].kind != thinkingEntry || m.entries[last].text != "done" {
		t.Fatalf("reasoning behind the final answer should sit just above it\n%s", screen(m))
	}
}

func TestExpandedReasoningKeepsSectionsApart(t *testing.T) {
	m := newMockModel(t)
	m.Update(agentEventMsg{event: agent.RequestSent{}})
	m.Update(agentEventMsg{event: agent.ReasoningDelta{NewPart: true}})
	m.Update(agentEventMsg{event: agent.ReasoningDelta{Text: "**One**"}})
	m.Update(agentEventMsg{event: agent.ReasoningDelta{NewPart: true}})
	m.Update(agentEventMsg{event: agent.ReasoningDelta{Text: "**Two**"}})
	m.Update(agentEventMsg{event: agent.ToolStarted{CallID: "c1", Name: "bash", Summary: "ls"}})
	m.Update(agentEventMsg{event: agent.ToolFinished{CallID: "c1"}})
	openAll(m)
	if !strings.Contains(screen(m), "│ One\n    │ \n    │ Two") {
		t.Fatalf("sections should be separated by a blank line\n%s", screen(m))
	}
}

func TestHeadingNeedsOneBoldRunOnItsLine(t *testing.T) {
	for summary, want := range map[string]string{
		"**Plan**\n\nbody":              "Plan",
		"**Plan**\n\n**Next**":          "Next",
		"**a** text **b**":              "",
		"**Plan**\n\n**Partial heading": "Plan",
	} {
		if got := latestHeading(summary); got != want {
			t.Errorf("%q: got %q, want %q", summary, got, want)
		}
	}
}

func TestModelCommandTakesASlugTheListLeavesOut(t *testing.T) {
	s := openStore(t)
	m := newSignedOutModel(t, s, listedModels{{Slug: "gpt-listed", DisplayName: "Listed"}})
	_, cmd := m.Update(loginDoneMsg{creds: auth.Credentials{Email: "me@example.com"}})
	drain(m, cmd)

	typeText(m, "/model gpt-unlisted")
	press(m, enter)
	if m.modelName != "gpt-unlisted" || m.picker != nil || !strings.Contains(screen(m), "Using gpt-unlisted.") {
		t.Fatalf("model=%q picker open=%v\n%s", m.modelName, m.picker != nil, screen(m))
	}

	typeText(m, "/model gpt-listed")
	press(m, enter)
	if m.modelName != "gpt-listed" || !strings.Contains(screen(m), "Using Listed.") {
		t.Fatalf("a listed slug should use its display name, model=%q\n%s", m.modelName, screen(m))
	}
}

func stepRun(t *testing.T) *Model {
	t.Helper()
	m := newMockModel(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	m.now = clock.Now
	event := func(e agent.Event) { m.Update(agentEventMsg{event: e}) }
	event(agent.RequestSent{})
	clock.advance(3 * time.Second)
	event(agent.ToolStarted{CallID: "c1", Name: "bash", Summary: "ls"})
	event(agent.ToolFinished{CallID: "c1", Output: "first-output"})
	event(agent.RequestSent{})
	event(agent.ReasoningDelta{Text: "**Checking**\n\nsecond-reasoning"})
	event(agent.ToolStarted{CallID: "c2", Name: "bash", Summary: "pwd"})
	event(agent.ToolFinished{CallID: "c2", Output: "second-output"})
	event(agent.TextDelta{Text: "done"})
	return m
}

func TestPastStepsOpenOneAtATime(t *testing.T) {
	m := stepRun(t)
	altUp := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt}

	press(m, altUp)
	if out := screen(m); !strings.Contains(out, "▸ ✓ Ran pwd") || strings.Contains(out, "second-output") {
		t.Fatalf("alt+up should highlight the newest step without opening it\n%s", out)
	}
	press(m, enter)
	if out := screen(m); !strings.Contains(out, "│ second-output") || strings.Contains(out, "first-output") {
		t.Fatalf("enter should open only the highlighted step\n%s", out)
	}

	press(m, altUp, altUp, enter)
	if out := screen(m); !strings.Contains(out, "│ first-output") || !strings.Contains(out, "│ second-output") {
		t.Fatalf("moving up and pressing enter should open the earlier step too\n%s", out)
	}
	press(m, enter)
	if strings.Contains(screen(m), "first-output") {
		t.Fatal("enter again should close it")
	}

	press(m, esc)
	if m.selected != -1 || strings.Contains(screen(m), "▸") {
		t.Fatal("esc should let go of the highlight")
	}
	if !strings.Contains(screen(m), "│ second-output") {
		t.Fatal("letting go should leave opened steps open")
	}
}

func TestThinkingWithoutReasoningNeverStays(t *testing.T) {
	m := stepRun(t)
	if thinkingSteps(m) != 0 || m.entries[0].tool.thought != nil || m.entries[1].tool.thought == nil {
		t.Fatalf("only the call with reasoning behind it should carry a thought\n%s", screen(m))
	}
}

func TestSelectionScrollsIntoView(t *testing.T) {
	m := newMockModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 18})
	for range 3 {
		typeText(m, "look around")
		press(m, enter)
	}
	for range 40 {
		press(m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	}
	if m.selected < 0 || !strings.Contains(screen(m), "▸") {
		t.Fatalf("the highlighted step should always be on screen\n%s", screen(m))
	}
}

func TestTypingKeepsEnterForSending(t *testing.T) {
	m := stepRun(t)
	press(m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	typeText(m, "hello")
	press(m, enter)
	if !slices.ContainsFunc(m.entries, func(e entry) bool { return e.kind == userEntry && e.text == "hello" }) {
		t.Fatal("with text in the input, enter should send it rather than open a step")
	}
}

func BenchmarkViewLongSessionAllStepsOpen(b *testing.B) {
	m := New(Deps{Agent: agent.New(&mock.Model{}), Models: mock.Models{}, Dir: "/tmp"})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	output := strings.Repeat("some line of tool output that is fairly long\n", 200)
	for i := range 200 {
		id := fmt.Sprint(i)
		m.Update(agentEventMsg{event: agent.RequestSent{}})
		m.Update(agentEventMsg{event: agent.ReasoningDelta{Text: "**Step**\n\nreasoning about the next step in some detail"}})
		m.Update(agentEventMsg{event: agent.ToolStarted{CallID: id, Name: "bash", Summary: "ls -la"}})
		m.Update(agentEventMsg{event: agent.ToolFinished{CallID: id, Output: output}})
		m.Update(agentEventMsg{event: agent.TextDelta{Text: "Some **markdown** answer text.\n"}})
	}
	openAll(m)
	b.ResetTimer()
	for range b.N {
		m.View()
	}
}

func TestWorkingShowsWheneverATurnGoesQuiet(t *testing.T) {
	m := newMockModel(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	m.now = clock.Now
	m.running, m.lastActivity = true, clock.now
	event := func(e agent.Event) { m.Update(agentEventMsg{event: e}) }

	event(agent.RequestSent{})
	event(agent.TextDelta{Text: "I will read the config."})
	clock.advance(200 * time.Millisecond)
	if strings.Contains(screen(m), "Working ·") {
		t.Fatalf("text that just arrived is progress enough\n%s", screen(m))
	}
	clock.advance(time.Second)
	if !strings.Contains(screen(m), "Working · 1s") {
		t.Fatalf("after the preamble, while the tool call is still coming, the turn must not look done\n%s", screen(m))
	}

	event(agent.ToolStarted{CallID: "c1", Name: "bash", Summary: "cat config.toml"})
	clock.advance(2 * time.Second)
	if strings.Contains(screen(m), "Working ·") {
		t.Fatal("a running tool already shows progress")
	}
	event(agent.ToolFinished{CallID: "c1"})
	event(agent.RequestSent{})
	clock.advance(2 * time.Second)
	if strings.Contains(screen(m), "Working ·") {
		t.Fatal("an open thinking step already shows progress")
	}

	m.Update(turnDoneMsg{})
	clock.advance(5 * time.Second)
	if strings.Contains(screen(m), "Working ·") {
		t.Fatal("a finished turn must not say it is working")
	}
}

func TestDraggingCopiesTheSelectedText(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "look around")
	press(m, enter)
	y := rowOf(m, "Want me to dig into any of these?")
	line := strings.Split(screen(m), "\n")[y]
	start := strings.Index(line, "Want")

	m.Update(tea.MouseClickMsg{X: start, Y: y - 2, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: start + 3, Y: y, Button: tea.MouseLeft})
	if m.drag == nil || !strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("the selection should be highlighted while dragging")
	}
	cmd := func() tea.Cmd {
		_, c := m.Update(tea.MouseReleaseMsg{X: start + 3, Y: y, Button: tea.MouseLeft})
		return c
	}()
	if cmd == nil || m.drag != nil {
		t.Fatal("releasing a drag should copy and end the selection")
	}
	lines := strings.Split(m.copied, "\n")
	if len(lines) != 3 || strings.TrimSpace(lines[0]) != "}" || lines[1] != "" || strings.TrimSpace(lines[2]) != "Want" {
		t.Fatalf("copied %q", m.copied)
	}
	if !strings.Contains(screen(m), "copied") {
		t.Fatalf("the status line should confirm the copy\n%s", screen(m))
	}
}

func TestDraggingDoesNotOpenSteps(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "look around")
	press(m, enter)
	y := rowOf(m, "list_files")
	drag(m, cell{3, y}, cell{20, y})
	if strings.Contains(screen(m), "go.sum") {
		t.Fatal("a drag over a step should select text, not open the step")
	}
	if !strings.Contains(m.copied, "list_files") {
		t.Fatalf("copied %q", m.copied)
	}
}

func TestSelectedTextSpansRows(t *testing.T) {
	rows := []string{"abcdef", "ghijkl", "mnopqr"}
	got := selectedText(rows, span{from: cell{4, 0}, to: cell{1, 2}}, 6)
	if got != "ef\nghijkl\nmn" {
		t.Fatalf("got %q", got)
	}
	backwards := selectedText(rows, span{from: cell{1, 2}, to: cell{4, 0}}, 6)
	if backwards != got {
		t.Fatal("dragging upward should select the same text")
	}
}
