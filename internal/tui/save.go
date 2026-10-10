// Saving the conversation after every turn.
// saveSession copies the agent's history once a turn has ended and writes it to the store through
// session in the background, giving the conversation an ID the first time. saveQueue keeps those
// background writes in order, so a slow save never overwrites a newer one. Sessions saved by
// `loop run` live in the same store and show up in /resume (resume.go) too.

package tui

import (
	"context"
	"fmt"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/rzhang57/loop/internal/session"
)

// saveQueue runs one conversation's saves one at a time and skips any that a newer save has
// already overtaken. Each conversation gets its own, so a late save is never dropped for another's.
type saveQueue struct {
	// made counts the saves handed out, and is only touched on the UI thread.
	made    int
	mu      sync.Mutex
	written int
}

// write runs save, the order-th save made, unless a later one was written first.
func (q *saveQueue) write(order int, save func() error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if order < q.written {
		return nil
	}
	q.written = order
	return save()
}

// saveSession returns a command that saves the conversation so far, or nil when there is nothing
// to save or nowhere to save it.
func (m *Model) saveSession() tea.Cmd {
	s := m.deps.Store
	items := m.deps.Agent.History()
	if s == nil || len(items) == 0 {
		return nil
	}
	now := m.now()
	if m.sessionID == "" {
		m.sessionID = session.NewID(now)
	}
	sess := session.Session{ID: m.sessionID, Model: m.modelName, Dir: m.deps.Dir, Updated: now, Items: items}
	m.saves.made++
	order, queue := m.saves.made, m.saves
	return func() tea.Msg {
		if err := queue.write(order, func() error { return session.Save(context.Background(), s, sess) }); err != nil {
			return errorMsg{err: fmt.Errorf("save conversation: %w", err)}
		}
		return nil
	}
}
