// Reopening saved conversations.
// /resume lists saved sessions (listSessions) into the picker, newest first, and loadSession reads
// the chosen one, or one named by ID. resumeSession then hands its history back to the agent,
// switches to its model for this window only and rebuilds the transcript (restore.go). Neither
// happens while a turn runs or a message is waiting to be sent.

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rzhang57/loop/internal/session"
)

const maxTitleWidth = 50

var (
	errNoSavedSessions    = errors.New("this chat doesn't save conversations")
	errResumeWhileRunning = errors.New("wait for the current turn to finish, or press esc, before resuming")
)

type (
	sessionsMsg struct {
		sessions []session.Summary
		err      error
	}
	sessionLoadedMsg struct {
		id   string
		sess session.Session
		ok   bool
		err  error
	}
)

// listSessions loads the saved sessions in the background for the picker.
func (m *Model) listSessions() tea.Cmd {
	s := m.deps.Store
	if s == nil {
		m.addError(errNoSavedSessions)
		return nil
	}
	return func() tea.Msg {
		sessions, err := session.List(context.Background(), s)
		return sessionsMsg{sessions: sessions, err: err}
	}
}

// loadSession reads one saved session in the background so it can be resumed.
func (m *Model) loadSession(id string) tea.Cmd {
	s := m.deps.Store
	if s == nil {
		m.addError(errNoSavedSessions)
		return nil
	}
	return func() tea.Msg {
		sess, ok, err := session.Load(context.Background(), s, id)
		return sessionLoadedMsg{id: id, sess: sess, ok: ok, err: err}
	}
}

// handleSessions opens the picker over the saved sessions, newest first.
func (m *Model) handleSessions(msg sessionsMsg) {
	switch {
	case msg.err != nil:
		m.addError(fmt.Errorf("list conversations: %w", msg.err))
		return
	case len(msg.sessions) == 0:
		m.add(entry{kind: noticeEntry, text: "No saved conversations yet."})
		return
	case m.busy():
		m.addError(errResumeWhileRunning)
		return
	}
	sessions := msg.sessions
	rows := make([]pickerRow, len(sessions))
	for i, s := range sessions {
		var details []string
		if !s.Updated.IsZero() {
			details = append(details, formatAge(m.now(), s.Updated))
		}
		if s.Model != "" {
			details = append(details, s.Model)
		}
		if s.Dir != "" && s.Dir != m.deps.Dir {
			details = append(details, shortPath(s.Dir))
		}
		title := s.Title
		if title == "" {
			title = s.ID
		}
		rows[i] = pickerRow{label: truncateText(title, maxTitleWidth), detail: strings.Join(details, " · "), current: s.ID == m.sessionID}
	}
	m.picker = &picker{
		title:  "Resume a conversation",
		rows:   rows,
		choose: func(i int) tea.Cmd { return m.loadSession(sessions[i].ID) },
	}
}

// resumeSession replaces the conversation on screen and in the agent with the loaded one.
func (m *Model) resumeSession(msg sessionLoadedMsg) {
	switch {
	case msg.err != nil:
		m.addError(msg.err)
		return
	case !msg.ok:
		m.addError(fmt.Errorf("no saved conversation %s", msg.id))
		return
	case m.busy():
		m.addError(errResumeWhileRunning)
		return
	}
	sess := msg.sess
	m.deps.Agent.Restore(sess.Items)
	m.sessionID, m.saves = sess.ID, &saveQueue{}
	m.entries = restoredEntries(sess.Items, m.deps.Agent.Summary)
	m.scroll, m.selected = 0, -1
	notice := "Resumed conversation " + sess.ID + "."
	if sess.Model != "" {
		m.modelName = sess.Model
		notice += " Using " + m.modelInfo(sess.Model).DisplayName + "."
	}
	m.add(entry{kind: noticeEntry, text: notice})
}

// busy reports whether a turn is running or a message is waiting to start one, either of which
// resuming would pull the conversation out from under.
func (m *Model) busy() bool {
	return m.running || m.pending != ""
}

// formatAge says how long before now t was, roughly, or gives the date once it is a week old.
func formatAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2 2006")
	}
}
