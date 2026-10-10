// Opening past steps to look inside them, one at a time.
// A click on a tool call or thinking step opens or closes it (clickStep). From the keyboard, with
// the input empty, alt+up and alt+down move a highlight between steps, enter opens or closes the
// highlighted one, and esc lets go of it. moveSelection also scrolls the transcript so the
// highlighted step stays on screen.

package tui

// moveSelection highlights the next step up (delta -1) or down (delta 1). With nothing highlighted
// it starts from the newest step.
func (m *Model) moveSelection(delta int) {
	i := m.selected
	if i < 0 {
		i = len(m.entries)
		if delta > 0 {
			return
		}
	}
	for i += delta; i >= 0 && i < len(m.entries); i += delta {
		if isStep(m.entries[i]) {
			m.selected = i
			m.revealSelection()
			return
		}
	}
}

// toggleSelected opens the highlighted step if it is closed and closes it if it is open.
func (m *Model) toggleSelected() {
	e := &m.entries[m.selected]
	e.expanded = !e.expanded
	m.revealSelection()
}

// revealSelection scrolls just enough to bring the highlighted step's first line into view.
func (m *Model) revealSelection() {
	lines, starts := m.transcript()
	height := m.transcriptHeight()
	row := starts[m.selected]
	bottom := max(len(lines)-height, 0)
	top := bottom - m.scroll
	switch {
	case row < top:
		m.scroll = bottom - row
	case row >= top+height:
		m.scroll = max(bottom-(row-height+1), 0)
	}
}

// clickStep opens or closes the step drawn on screen row y, if there is one.
func (m *Model) clickStep(y int) {
	lines, starts := m.transcript()
	height := m.transcriptHeight()
	if y >= height {
		return
	}
	row := max(len(lines)-height, 0) - m.scroll + y
	for i := len(starts) - 1; i >= 0; i-- {
		if starts[i] <= row {
			if isStep(m.entries[i]) {
				m.entries[i].expanded = !m.entries[i].expanded
			}
			return
		}
	}
}
