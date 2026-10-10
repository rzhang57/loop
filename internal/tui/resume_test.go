package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/mock"
	"github.com/rzhang57/loop/internal/session"
	"github.com/rzhang57/loop/internal/store"
)

// newSavingModel is a mock chat that saves its conversations to s, as a signed-in chat would.
func newSavingModel(t *testing.T, s *store.Store, deps Deps) *Model {
	t.Helper()
	var tools []agent.Tool
	for _, tool := range mock.Tools() {
		tool.Took = 5 * time.Millisecond
		tools = append(tools, tool)
	}
	deps.Agent = agent.New(&mock.Model{}, tools...)
	deps.Models, deps.Store, deps.Dir = mock.Models{}, s, "/work"
	m := New(deps)
	m.modelName = "mock"
	styles := m.input.Styles()
	styles.Cursor.Blink = false
	m.input.SetStyles(styles)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	drain(m, m.Init())
	return m
}

func savedSessions(t *testing.T, s *store.Store) []session.Summary {
	t.Helper()
	sessions, err := session.List(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return sessions
}

func TestEachFinishedTurnIsSaved(t *testing.T) {
	s := openStore(t)
	m := newSavingModel(t, s, Deps{})
	if len(savedSessions(t, s)) != 0 {
		t.Fatal("an empty chat should not be saved")
	}
	typeText(m, "look around")
	press(m, enter)

	sessions := savedSessions(t, s)
	if len(sessions) != 1 || sessions[0].Title != "look around" || sessions[0].Model != "mock" || sessions[0].Dir != "/work" {
		t.Fatalf("saved %+v", sessions)
	}
	sess, ok, err := session.Load(context.Background(), s, sessions[0].ID)
	if err != nil || !ok || len(sess.Items) != len(m.deps.Agent.History()) {
		t.Fatalf("saved %d items, agent has %d (ok=%v err=%v)", len(sess.Items), len(m.deps.Agent.History()), ok, err)
	}

	typeText(m, "and again")
	press(m, enter)
	if sessions := savedSessions(t, s); len(sessions) != 1 {
		t.Fatalf("a second turn should update the same session, got %d", len(sessions))
	}
}

func TestStoppedTurnIsSavedToo(t *testing.T) {
	s := openStore(t)
	m := newSavingModel(t, s, Deps{})
	m.deps.Agent = agent.New(&mock.Model{WordDelay: time.Second})
	typeText(m, "hello")
	_, cmd := m.Update(enter)
	m.Update(esc)
	drain(m, cmd)
	if sessions := savedSessions(t, s); len(sessions) != 1 || sessions[0].Title != "hello" {
		t.Fatalf("saved %+v", sessions)
	}
}

func TestResumePickerReopensAConversation(t *testing.T) {
	s := openStore(t)
	first := newSavingModel(t, s, Deps{})
	typeText(first, "look around")
	press(first, enter)
	typeText(first, "/clear")
	press(first, enter)
	typeText(first, "second chat")
	press(first, enter)
	if len(savedSessions(t, s)) != 2 {
		t.Fatal("/clear should start a new saved conversation and keep the old one")
	}

	m := newSavingModel(t, s, Deps{})
	m.modelName = "mock-fast"
	typeText(m, "/resume")
	press(m, enter)
	out := screen(m)
	if m.picker == nil || !strings.Contains(out, "Resume a conversation") {
		t.Fatalf("/resume should open the picker\n%s", out)
	}
	if strings.Index(out, "second chat") > strings.Index(out, "look around") {
		t.Fatalf("newest conversation should come first\n%s", out)
	}

	recorder := &recordingModel{}
	press(m, down, enter)
	out = screen(m)
	for _, want := range []string{"› look around", "✓ list_files .", "✗ fetch_page https://example.com/docs · connection refused", "Want me to dig into any of these?"} {
		if !strings.Contains(out, want) {
			t.Errorf("resumed transcript is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "✓ list_files . ·") {
		t.Errorf("restored steps should not show a duration\n%s", out)
	}
	if m.modelName != "mock" {
		t.Fatalf("resume should use the conversation's model, got %q", m.modelName)
	}

	restored := len(m.deps.Agent.History())
	if saved, _, _ := session.Load(context.Background(), s, m.sessionID); restored == 0 || restored != len(saved.Items) {
		t.Fatalf("agent holds %d items after resuming, the session has %d", restored, len(saved.Items))
	}
	m.deps.Agent = agentWithHistory(recorder, m.deps.Agent.History())
	typeText(m, "one more")
	press(m, enter)
	if got := len(recorder.inputs[0]); got != restored+1 {
		t.Fatalf("model saw %d items, want the %d restored plus the new message", got, restored)
	}
	if sessions := savedSessions(t, s); len(sessions) != 2 || sessions[0].Title != "look around" {
		t.Fatalf("continuing should update the resumed session, got %+v", sessions)
	}
}

func agentWithHistory(model llm.Model, items []llm.Item) *agent.Agent {
	a := agent.New(model)
	a.Restore(items)
	return a
}

func TestResumeByIDAndFromTheStart(t *testing.T) {
	s := openStore(t)
	first := newSavingModel(t, s, Deps{})
	typeText(first, "look around")
	press(first, enter)
	id := savedSessions(t, s)[0].ID

	m := newSavingModel(t, s, Deps{})
	typeText(m, "/resume "+id)
	press(m, enter)
	if m.sessionID != id || !strings.Contains(screen(m), "› look around") {
		t.Fatalf("/resume ID should open it\n%s", screen(m))
	}

	typeText(m, "/resume nope")
	press(m, enter)
	if m.sessionID != id || !strings.Contains(screen(m), "no saved conversation nope") {
		t.Fatalf("an unknown ID should be an error and keep the conversation\n%s", screen(m))
	}

	opened := newSavingModel(t, s, Deps{ResumeID: id})
	if opened.sessionID != id || !strings.Contains(screen(opened), "› look around") {
		t.Fatalf("starting with an ID should open it\n%s", screen(opened))
	}
	picking := newSavingModel(t, s, Deps{PickSession: true})
	if picking.picker == nil || !strings.Contains(screen(picking), "Resume a conversation") {
		t.Fatalf("starting with the picker should show it\n%s", screen(picking))
	}
}

func TestResumePickerEscapeKeepsTheConversation(t *testing.T) {
	s := openStore(t)
	first := newSavingModel(t, s, Deps{})
	typeText(first, "look around")
	press(first, enter)

	m := newSavingModel(t, s, Deps{})
	typeText(m, "hello")
	press(m, enter)
	id, entries := m.sessionID, len(m.entries)
	typeText(m, "/resume")
	press(m, enter, esc)
	if m.picker != nil || m.sessionID != id || len(m.entries) != entries {
		t.Fatal("esc should close the picker and keep the current conversation")
	}
}

func TestResumeIsRefusedWhileATurnRuns(t *testing.T) {
	s := openStore(t)
	first := newSavingModel(t, s, Deps{})
	typeText(first, "look around")
	press(first, enter)
	id := savedSessions(t, s)[0].ID

	m := newSavingModel(t, s, Deps{})
	m.deps.Agent = agent.New(&mock.Model{WordDelay: time.Second})
	typeText(m, "hello")
	_, cmd := m.Update(enter)
	typeText(m, "/resume "+id)
	m.Update(enter)
	m.Update(sessionLoadedMsg{id: id, sess: session.Session{ID: id, Items: []llm.Item{llm.UserMessage("x")}}, ok: true})
	if m.sessionID == id || !strings.Contains(screen(m), "before resuming") {
		t.Fatalf("resuming must wait for the running turn\n%s", screen(m))
	}
	m.Update(sessionsMsg{sessions: []session.Summary{{ID: id}}})
	if m.picker != nil {
		t.Fatal("the picker must not open while a turn runs")
	}
	m.Update(esc)
	drain(m, cmd)
}

func TestMockModeDoesNotSave(t *testing.T) {
	m := newMockModel(t)
	typeText(m, "/resume")
	press(m, enter)
	if !strings.Contains(screen(m), "this chat doesn't save conversations") {
		t.Fatalf("got\n%s", screen(m))
	}
}

func TestResumePickerShowsAgeModelAndOtherDirectories(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	for _, sess := range []session.Session{
		{ID: "a", Model: "gpt-x", Dir: "/work", Updated: now.Add(-2 * time.Hour), Items: []llm.Item{llm.UserMessage("here")}},
		{ID: "b", Model: "gpt-y", Dir: "/elsewhere", Updated: now.Add(-3 * 24 * time.Hour), Items: []llm.Item{llm.UserMessage("there")}},
	} {
		if err := session.Save(ctx, s, sess); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set(ctx, "session.untimed", `{"model":"gpt-z","items":[]}`); err != nil {
		t.Fatal(err)
	}

	m := newSavingModel(t, s, Deps{})
	m.now = func() time.Time { return now }
	typeText(m, "/resume")
	press(m, enter)
	out := screen(m)
	for _, want := range []string{"here  2h ago · gpt-x\n", "there  3d ago · gpt-y · /elsewhere", "untimed  gpt-z\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker is missing %q\n%s", want, out)
		}
	}
}

func TestPickerShowsAWindowAroundTheHighlight(t *testing.T) {
	rows := make([]pickerRow, 30)
	for i := range rows {
		rows[i] = pickerRow{label: fmt.Sprintf("row%02d", i)}
	}
	p := &picker{title: "t", rows: rows, selected: 20}
	out := ansi.Strip(strings.Join(p.lines(), "\n"))
	if len(p.lines()) != 2+maxPickerRows || !strings.Contains(out, "› row20") || strings.Contains(out, "row00") {
		t.Fatalf("got\n%s", out)
	}
	p.selected = len(rows) - 1
	out = ansi.Strip(strings.Join(p.lines(), "\n"))
	if len(p.lines()) != 2+maxPickerRows || !strings.Contains(out, "› row29") || strings.Contains(out, "row19") {
		t.Fatalf("the last row should end the window\n%s", out)
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{30 * 24 * time.Hour, "Sep 8"},
		{400 * 24 * time.Hour, "Sep 3 2025"},
	} {
		if got := formatAge(now, now.Add(-tc.ago)); got != tc.want {
			t.Errorf("%v ago: got %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestResumeKeepsTheSavedDefaultModel(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	s.Set(ctx, keyModel, "mock-fast")
	session.Save(ctx, s, session.Session{ID: "a", Model: "mock", Items: []llm.Item{llm.UserMessage("hi")}})

	m := newSavingModel(t, s, Deps{ResumeID: "a"})
	if m.modelName != "mock" {
		t.Fatalf("resumed with %q, want the session's model", m.modelName)
	}
	if saved, _, _ := s.Get(ctx, keyModel); saved != "mock-fast" {
		t.Fatalf("resuming changed the saved default to %q", saved)
	}
}

func TestResumeWithNothingSaved(t *testing.T) {
	m := newSavingModel(t, openStore(t), Deps{PickSession: true})
	press(m, enter)
	if m.picker != nil || !strings.Contains(screen(m), "No saved conversations yet.") {
		t.Fatalf("got\n%s", screen(m))
	}
}

func TestResumeErrorsAreShown(t *testing.T) {
	s := openStore(t)
	s.Set(context.Background(), "session.bad", "{not json")
	m := newSavingModel(t, s, Deps{ResumeID: "bad"})
	if m.sessionID != "" || !strings.Contains(screen(m), "read session bad") {
		t.Fatalf("a corrupt session should be reported\n%s", screen(m))
	}

	s.Close()
	typeText(m, "/resume")
	press(m, enter)
	if !strings.Contains(screen(m), "list conversations:") {
		t.Fatalf("a failed listing should be reported\n%s", screen(m))
	}
	typeText(m, "hello")
	press(m, enter)
	if !strings.Contains(screen(m), "save conversation:") {
		t.Fatalf("a failed save should be reported\n%s", screen(m))
	}
}

func TestResumeWaitsForAPendingMessage(t *testing.T) {
	s := openStore(t)
	session.Save(context.Background(), s, session.Session{ID: "a", Items: []llm.Item{llm.UserMessage("old")}})
	m := newSavingModel(t, s, Deps{})
	m.pending = "new message"
	m.Update(sessionLoadedMsg{id: "a", sess: session.Session{ID: "a", Items: []llm.Item{llm.UserMessage("old")}}, ok: true})
	m.Update(sessionsMsg{sessions: []session.Summary{{ID: "a"}}})
	if m.sessionID == "a" || m.picker != nil || !strings.Contains(screen(m), "before resuming") {
		t.Fatalf("resuming must wait for the message waiting on a model\n%s", screen(m))
	}
}

func TestSaveQueueSkipsOvertakenSaves(t *testing.T) {
	q := &saveQueue{}
	var wrote []int
	for _, order := range []int{1, 3, 2, 4} {
		q.write(order, func() error { wrote = append(wrote, order); return nil })
	}
	if !reflect.DeepEqual(wrote, []int{1, 3, 4}) {
		t.Fatalf("wrote %v, want the late save 2 skipped", wrote)
	}
}
