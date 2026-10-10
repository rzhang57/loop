// The input box and slash commands.
// submit sends a typed message as a new turn, or hands a /command to runCommand, which carries out
// /login, /logout, /model, /clear, /resume, /help and /exit. /clear starts a new saved conversation
// and /resume reopens one (resume.go). suggestions lists the commands matching what has been
// typed so far, and login runs the browser sign-in in the background.

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type command struct {
	name        string
	description string
}

var commands = []command{
	{"login", "sign in with your ChatGPT account"},
	{"logout", "sign out"},
	{"model", "choose the model, or /model SLUG for one not listed"},
	{"clear", "start a new conversation"},
	{"resume", "reopen a saved conversation, or /resume ID for one"},
	{"help", "show commands and keys"},
	{"exit", "quit"},
}

// suggestions lists the commands matching a "/" typed at the start of the input.
func (m *Model) suggestions() []command {
	value := m.input.Value()
	if m.running || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \n") {
		return nil
	}
	var matches []command
	for _, c := range commands {
		if strings.HasPrefix(c.name, value[1:]) {
			matches = append(matches, c)
		}
	}
	return matches
}

func (m *Model) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.running || m.pending != "" {
		return nil
	}
	if name, ok := strings.CutPrefix(text, "/"); ok {
		m.input.Reset()
		return m.runCommand(name)
	}
	if !m.deps.mock() && m.email == "" {
		m.addError(errSignInFirst)
		return nil
	}
	m.input.Reset()
	m.scroll = 0
	m.add(entry{kind: userEntry, text: text})
	if m.modelName == "" {
		m.pending = text
		return m.fetchModels()
	}
	return m.startTurn(text)
}

func (m *Model) runCommand(text string) tea.Cmd {
	name, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "login":
		if m.deps.mock() {
			m.add(entry{kind: noticeEntry, text: "Mock mode doesn't sign in."})
			return nil
		}
		if m.cancelLogin != nil {
			m.add(entry{kind: noticeEntry, text: "A sign-in is already open in your browser. Press esc to cancel it."})
			return nil
		}
		return m.login()
	case "logout":
		if m.deps.mock() {
			m.add(entry{kind: noticeEntry, text: "Mock mode doesn't sign in."})
			return nil
		}
		authClient := m.deps.Auth
		return func() tea.Msg { return logoutDoneMsg{err: authClient.Logout(context.Background())} }
	case "model":
		if !m.deps.mock() && m.email == "" {
			m.addError(errSignInFirst)
			return nil
		}
		if arg != "" {
			return m.chooseModel(m.modelInfo(arg))
		}
		if len(m.models) > 0 {
			m.openPicker()
			return nil
		}
		m.pickerRequested = true
		return m.fetchModels()
	case "clear":
		m.deps.Agent.Reset()
		m.entries, m.scroll, m.selected = nil, 0, -1
		m.sessionID, m.saves = "", &saveQueue{}
		return nil
	case "resume":
		if arg != "" {
			return m.loadSession(arg)
		}
		return m.listSessions()
	case "help":
		var b strings.Builder
		for _, c := range commands {
			fmt.Fprintf(&b, "/%-8s %s\n", c.name, c.description)
		}
		b.WriteString("\nenter send · shift+enter new line · esc stop · ctrl+c quit\nclick a tool call or thinking step to open or close it, or pick one with alt+↑ / alt+↓ and press enter\nscroll, pgup/pgdown, or up/down with an empty input to move · drag to select and copy text")
		m.add(entry{kind: noticeEntry, text: b.String()})
		return nil
	case "exit":
		return tea.Quit
	}
	m.addError(fmt.Errorf("unknown command /%s, try /help", text))
	return nil
}

func (m *Model) login() tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.cancelLogin = cancel
	src := make(chan tea.Msg, 4)
	authClient := m.deps.Auth
	go func() {
		defer close(src)
		defer cancel()
		creds, err := authClient.Login(ctx, func(url string) { src <- loginURLMsg{url: url} })
		src <- loginDoneMsg{creds: creds, err: err}
	}()
	return listen(src)
}
