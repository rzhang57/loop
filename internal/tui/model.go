// Package tui is the full-screen terminal chat, built on Bubble Tea.
// This file holds the Model, its dependencies and the Update loop that routes each message to the
// file that owns it: keys.go handles key presses and scrolling, selection.go mouse clicks and copying,
// steps.go opening past steps, commands.go the input box and slash commands, turn.go running a turn
// and turning agent events into entries (entries.go), models.go the model list, picker.go the list
// picker, save.go saving the conversation, resume.go and restore.go reopening saved ones, and
// view.go plus markdown.go the rendering. Background work streams back as messages through listen.
// Opened by cmd/loop.
package tui

import (
	"context"
	"errors"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/auth"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/store"
)

const (
	keyModel       = "model"
	preferredModel = "gpt-6.1-sol"
	frameInterval  = 80 * time.Millisecond
)

var errSignInFirst = errors.New("not signed in, run /login first")

type ModelLister interface {
	ListModels(ctx context.Context) ([]llm.ModelInfo, error)
}

// Deps is what the interface drives. A nil Auth means mock mode; a nil Store means nothing is saved.
// ResumeID opens that saved conversation on start, and PickSession opens the list of saved ones.
type Deps struct {
	Agent       *agent.Agent
	Auth        *auth.Client
	Models      ModelLister
	Store       *store.Store
	Dir         string
	ResumeID    string
	PickSession bool
}

func (d Deps) mock() bool {
	return d.Auth == nil
}

// streamed carries one message from a background task along with the channel to keep listening on.
type streamed struct {
	msg tea.Msg
	src chan tea.Msg
}

type (
	agentEventMsg struct{ event agent.Event }
	turnDoneMsg   struct{ err error }
	loginURLMsg   struct{ url string }
	loginDoneMsg  struct {
		creds auth.Credentials
		err   error
	}
	logoutDoneMsg struct{ err error }
	modelsMsg     struct {
		models []llm.ModelInfo
		err    error
	}
	errorMsg struct{ err error }
	tickMsg  struct{ turn int }
)

type Model struct {
	deps Deps

	width, height int
	input         textarea.Model
	md            markdown

	entries     []entry
	running     bool
	cancelTurn  context.CancelFunc
	cancelLogin context.CancelFunc
	turn        int
	frame       int
	scroll      int
	selected    int

	suggestion      int
	picker          *picker
	models          []llm.ModelInfo
	pickerRequested bool
	pending         string

	modelName string
	email     string
	// sessionID is the saved conversation this one is written to, empty until it is first saved.
	sessionID string
	saves     *saveQueue
	thinking  *thought
	// lastActivity is when the running turn last showed any sign of progress.
	lastActivity time.Time
	// drag is the text selection in progress; copied is the last text copied, shown briefly.
	drag     *span
	copied   string
	copiedAt time.Time
	now      func() time.Time
}

func New(deps Deps) *Model {
	input := textarea.New()
	input.Prompt = "› "
	input.Placeholder = "Ask for anything, or type / for commands"
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	input.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")
	styles := textarea.DefaultStyles(true)
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(accent)
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	input.SetStyles(styles)

	m := &Model{deps: deps, input: input, now: time.Now, selected: -1, saves: &saveQueue{}}
	ctx := context.Background()
	if deps.Store != nil {
		m.modelName, _, _ = deps.Store.Get(ctx, keyModel)
	}
	if deps.mock() {
		m.modelName = "mock"
	} else if creds, err := deps.Auth.Current(ctx); err == nil {
		m.email = creds.Email
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.input.Focus()}
	if !m.deps.mock() && m.email != "" {
		cmds = append(cmds, m.fetchModels())
	}
	switch {
	case m.deps.ResumeID != "":
		cmds = append(cmds, m.loadSession(m.deps.ResumeID))
	case m.deps.PickSession:
		cmds = append(cmds, m.listSessions())
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if s, ok := msg.(streamed); ok {
		model, cmd := m.Update(s.msg)
		return model, tea.Batch(cmd, listen(s.src))
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(m.width-4, 10))
		m.scrollBy(0)
		return m, nil

	case tea.MouseMsg:
		return m, m.handleMouse(msg)

	case clearCopiedMsg:
		if m.now().Sub(m.copiedAt) >= 2*time.Second {
			m.copied = ""
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tickMsg:
		if !m.running || msg.turn != m.turn {
			return m, nil
		}
		m.frame++
		return m, tick(m.turn)

	case agentEventMsg:
		m.applyAgentEvent(msg.event)
		return m, nil

	case turnDoneMsg:
		m.running, m.cancelTurn = false, nil
		m.flushThought()
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.add(entry{kind: noticeEntry, text: "Stopped."})
		case msg.err != nil:
			m.addError(msg.err)
			if sessionEnded(msg.err) {
				m.email = ""
			}
		}
		return m, m.saveSession()

	case sessionsMsg:
		m.handleSessions(msg)
		return m, nil

	case sessionLoadedMsg:
		m.resumeSession(msg)
		return m, nil

	case loginURLMsg:
		m.add(entry{kind: noticeEntry, text: "Opening your browser to sign in. If it doesn't open, visit:\n" + msg.url})
		return m, nil

	case loginDoneMsg:
		m.cancelLogin = nil
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.add(entry{kind: noticeEntry, text: "Sign-in cancelled."})
			return m, nil
		case errors.Is(msg.err, context.DeadlineExceeded):
			m.addError(errors.New("sign-in timed out, run /login to try again"))
			return m, nil
		case msg.err != nil:
			m.addError(msg.err)
			return m, nil
		}
		m.email = msg.creds.Email
		m.add(entry{kind: noticeEntry, text: "Signed in as " + msg.creds.Email + "."})
		return m, m.fetchModels()

	case logoutDoneMsg:
		m.email, m.models = "", nil
		switch {
		case errors.Is(msg.err, auth.ErrNotSignedIn):
			m.add(entry{kind: noticeEntry, text: "Not signed in."})
		case msg.err != nil:
			m.addError(msg.err)
		default:
			m.add(entry{kind: noticeEntry, text: "Signed out."})
		}
		return m, nil

	case modelsMsg:
		return m.handleModels(msg)

	case errorMsg:
		m.addError(msg.err)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) add(e entry) {
	m.entries = append(m.entries, e)
}

func (m *Model) addError(err error) {
	m.add(entry{kind: errorEntry, text: err.Error()})
}

// tick schedules the next animation frame for a turn; frames from earlier turns are ignored.
func tick(turn int) tea.Cmd {
	return tea.Tick(frameInterval, func(time.Time) tea.Msg { return tickMsg{turn: turn} })
}

func listen(src chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-src
		if !ok {
			return nil
		}
		return streamed{msg: msg, src: src}
	}
}
