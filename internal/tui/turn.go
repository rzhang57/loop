// Running a turn and showing its progress.
// startTurn calls agent.Agent.Send in a goroutine and streams its events back as messages.
// applyAgentEvent turns each event into transcript entries: text, tool calls and thinking steps,
// which open when a request goes out and close when the model writes or calls a tool. sessionEnded
// spots errors that mean the user must sign in again.

package tui

import (
	"context"
	"errors"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/auth"
	"github.com/rzhang57/loop/internal/llm"
)

func (m *Model) startTurn(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancelTurn, m.lastActivity = true, cancel, m.now()
	m.turn++
	src := make(chan tea.Msg, 64)
	a, model := m.deps.Agent, m.modelName
	go func() {
		defer close(src)
		err := a.Send(ctx, model, text, func(e agent.Event) { src <- agentEventMsg{event: e} })
		src <- turnDoneMsg{err: err}
	}()
	return tea.Batch(listen(src), tick(m.turn))
}

func (m *Model) applyAgentEvent(event agent.Event) {
	m.lastActivity = m.now()
	switch e := event.(type) {
	case agent.RequestSent:
		m.flushThought()
		m.thinking = &thought{started: m.now()}
	case agent.ResponseStarted:
		if m.thinking != nil {
			m.thinking.responded = true
		}
	case agent.ReasoningDelta:
		if m.thinking == nil {
			m.thinking = &thought{started: m.now(), responded: true}
		}
		if e.NewPart && m.thinking.summary != "" {
			m.thinking.summary += "\n\n"
		}
		m.thinking.summary += e.Text
	case agent.TextDelta:
		m.settleThought()
		if last := len(m.entries) - 1; last >= 0 && m.entries[last].kind == assistantEntry {
			m.entries[last].text += e.Text
			return
		}
		m.add(entry{kind: assistantEntry, text: e.Text})
	case agent.ToolStarted:
		m.add(entry{kind: toolEntry, tool: &toolCall{id: e.CallID, name: e.Name, summary: e.Summary, thought: m.takeThought()}})
	case agent.ToolFinished:
		for i := len(m.entries) - 1; i >= 0; i-- {
			if t := m.entries[i].tool; t != nil && t.id == e.CallID {
				t.done, t.output, t.err, t.duration = true, e.Output, e.Err, e.Duration
				break
			}
		}
	}
}

// settleThought stops the thinking clock once the model starts answering, keeping the reasoning
// for the tool call that follows.
func (m *Model) settleThought() {
	if t := m.thinking; t != nil && !t.done {
		t.done, t.duration = true, m.now().Sub(t.started)
	}
}

// takeThought hands over the reasoning behind the next tool call, or nil if the model shared none.
func (m *Model) takeThought() *thought {
	m.settleThought()
	t := m.thinking
	m.thinking = nil
	if t == nil || t.summary == "" {
		return nil
	}
	return t
}

// flushThought keeps reasoning that led to no tool call, such as before the final answer, as one
// step placed above that answer.
func (m *Model) flushThought() {
	t := m.takeThought()
	if t == nil {
		return
	}
	e := entry{kind: thinkingEntry, thought: t}
	if last := len(m.entries) - 1; last >= 0 && m.entries[last].kind == assistantEntry {
		m.entries = slices.Insert(m.entries, last, e)
		return
	}
	m.add(e)
}

// sessionEnded reports whether err means the saved sign-in no longer works.
func sessionEnded(err error) bool {
	var apiErr *llm.APIError
	return errors.Is(err, auth.ErrSessionExpired) || errors.Is(err, auth.ErrNotSignedIn) ||
		(errors.As(err, &apiErr) && apiErr.Code == "subscription_sharing_invalid_user")
}
