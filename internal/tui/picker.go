// The list picker shown above the input, shared by /model and /resume.
// A picker holds its rows, the highlighted one and what choosing a row does. handlePickerKey moves
// the highlight, chooses with enter or closes with esc, and lines draws it, showing only the
// maxPickerRows rows around the highlight in a longer list.

package tui

import tea "charm.land/bubbletea/v2"

const maxPickerRows = 10

type picker struct {
	title    string
	hint     string
	rows     []pickerRow
	selected int
	// choose acts on the row at index i once the picker has closed.
	choose func(i int) tea.Cmd
}

type pickerRow struct {
	label   string
	detail  string
	current bool
}

// handlePickerKey moves through, chooses from or closes the open picker.
func (m *Model) handlePickerKey(key string) tea.Cmd {
	p := m.picker
	switch key {
	case "up", "ctrl+p":
		p.selected = max(p.selected-1, 0)
	case "down", "ctrl+n":
		p.selected = min(p.selected+1, len(p.rows)-1)
	case "enter":
		m.picker = nil
		return p.choose(p.selected)
	case "esc", "ctrl+c":
		m.picker = nil
	}
	return nil
}

// lines draws the picker's title and the rows around the highlighted one.
func (p *picker) lines() []string {
	lines := []string{"", "  " + bold.Render(p.title) + faint.Render("  ↑↓ move · enter select · esc cancel"+p.hint)}
	first := min(max(p.selected-maxPickerRows/2, 0), max(len(p.rows)-maxPickerRows, 0))
	for i := first; i < min(first+maxPickerRows, len(p.rows)); i++ {
		row := p.rows[i]
		marker, label := "    ", dim.Render(row.label)
		if i == p.selected {
			marker, label = "  "+accented.Render("› "), accented.Render(row.label)
		}
		current := ""
		if row.current {
			current = okStyle.Render(" ✓")
		}
		lines = append(lines, marker+label+faint.Render("  "+row.detail)+current)
	}
	return lines
}
