// Mouse input on the transcript: clicking steps and copying text.
// A press and release on the same cell is a click, which opens or closes the step there
// (clickStep in steps.go). Pressing and dragging selects text instead: the region is highlighted
// while dragging, and on release its plain text is copied to the clipboard through the terminal.
// The scroll wheel scrolls the transcript.

package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// cell is a position on screen, in columns and rows.
type cell struct{ x, y int }

// span is a stretch of screen text between two cells, in either order.
type span struct{ from, to cell }

type clearCopiedMsg struct{}

var highlight = lipgloss.NewStyle().Reverse(true)

// ordered returns the span's cells with the earlier one first.
func (s span) ordered() (cell, cell) {
	a, b := s.from, s.to
	if b.y < a.y || (b.y == a.y && b.x < a.x) {
		a, b = b, a
	}
	return a, b
}

// columns returns which columns of row y the span covers, as a half-open range.
func (s span) columns(y, width int) (int, int, bool) {
	a, b := s.ordered()
	if y < a.y || y > b.y {
		return 0, 0, false
	}
	left, right := 0, width
	if y == a.y {
		left = a.x
	}
	if y == b.y {
		right = b.x + 1
	}
	return left, right, left < right
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	mouse := msg.Mouse()
	at := cell{mouse.X, mouse.Y}
	switch msg.(type) {
	case tea.MouseClickMsg:
		if mouse.Button == tea.MouseLeft {
			m.drag = &span{from: at, to: at}
		}
	case tea.MouseMotionMsg:
		if m.drag != nil {
			m.drag.to = at
		}
	case tea.MouseReleaseMsg:
		if m.drag == nil {
			return nil
		}
		drag := *m.drag
		m.drag = nil
		if drag.from == drag.to {
			m.clickStep(at.y)
			return nil
		}
		text := selectedText(m.screenRows(), drag, m.width)
		if text == "" {
			return nil
		}
		m.copied = text
		m.copiedAt = m.now()
		return tea.Batch(tea.SetClipboard(text), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearCopiedMsg{} }))
	case tea.MouseWheelMsg:
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.scrollBy(3)
		case tea.MouseWheelDown:
			m.scrollBy(-3)
		}
	}
	return nil
}

// selectedText is the plain text the span covers on screen, one line per row, without the
// styling or the padding at the end of each row.
func selectedText(rows []string, s span, width int) string {
	var lines []string
	for y, row := range rows {
		left, right, ok := s.columns(y, width)
		if !ok {
			continue
		}
		lines = append(lines, strings.TrimRight(ansi.Strip(ansi.Cut(row, left, right)), " "))
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// highlightRows draws the span being dragged in reverse video so the selection is visible.
func highlightRows(rows []string, s span, width int) {
	for y, row := range rows {
		left, right, ok := s.columns(y, width)
		if !ok {
			continue
		}
		selected := ansi.Strip(ansi.Cut(row, left, right))
		rows[y] = ansi.Cut(row, 0, left) + highlight.Render(selected) + ansi.Cut(row, right, width)
	}
}
