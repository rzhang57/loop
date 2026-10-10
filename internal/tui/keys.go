// Key handling for the tui Model.
// handleKey sends each key press to whichever part of the screen owns it: the picker (picker.go)
// when it is open, the slash-command suggestions while typing a command, or the global keys (ctrl+c
// and esc to stop, page up and down and the arrow keys to scroll). Anything else goes to the input
// box.

package tui

import tea "charm.land/bubbletea/v2"

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.picker != nil {
		return m, m.handlePickerKey(key)
	}

	if matches := m.suggestions(); len(matches) > 0 {
		switch key {
		case "up", "ctrl+p":
			m.suggestion = max(m.suggestion-1, 0)
			return m, nil
		case "down", "ctrl+n":
			m.suggestion = min(m.suggestion+1, len(matches)-1)
			return m, nil
		case "tab":
			m.input.SetValue("/" + matches[m.suggestion].name)
			m.input.CursorEnd()
			return m, nil
		case "enter":
			m.input.Reset()
			return m, m.runCommand(matches[m.suggestion].name)
		}
	}

	switch key {
	case "ctrl+c":
		switch {
		case m.stop():
		case m.input.Value() != "":
			m.input.Reset()
		default:
			return m, tea.Quit
		}
		return m, nil
	case "esc":
		if !m.stop() {
			m.selected = -1
		}
		return m, nil
	case "pgup":
		m.scrollBy(max(m.transcriptHeight()-2, 1))
		return m, nil
	case "pgdown":
		m.scrollBy(-max(m.transcriptHeight()-2, 1))
		return m, nil
	case "enter":
		if m.selected >= 0 && m.input.Value() == "" {
			m.toggleSelected()
			return m, nil
		}
		return m, m.submit()
	case "alt+up", "alt+down":
		if m.input.Value() == "" {
			if key == "alt+up" {
				m.moveSelection(-1)
			} else {
				m.moveSelection(1)
			}
			return m, nil
		}
	case "up", "down":
		// The scroll wheel arrives as arrow keys in full-screen mode, so an empty input scrolls the transcript.
		if m.input.Value() == "" {
			if key == "up" {
				m.scrollBy(3)
			} else {
				m.scrollBy(-3)
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.suggestion = min(m.suggestion, max(len(m.suggestions())-1, 0))
	return m, cmd
}

// stop cancels whatever is in progress, a turn or a sign-in, and reports whether there was one.
func (m *Model) stop() bool {
	switch {
	case m.cancelTurn != nil:
		m.cancelTurn()
	case m.cancelLogin != nil:
		m.cancelLogin()
	default:
		return false
	}
	return true
}

// scrollBy moves the transcript up by delta rows, or down when negative, staying within its length.
func (m *Model) scrollBy(delta int) {
	lines, _ := m.transcript()
	m.scroll = min(max(m.scroll+delta, 0), max(len(lines)-m.transcriptHeight(), 0))
}
