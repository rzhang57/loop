// Rendering for the tui Model: turns its state into the screen each frame.
// View lays out the scrolled transcript, the command suggestions or picker panel, the input box
// and the status line. transcript renders each entry kind, with assistant text as markdown
// (markdown.go), one-line tool and thinking steps with spinners and a shimmer while running, and
// their full output or reasoning when a step is opened. Also holds the shared colors and styles.

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const maxDetailLines = 30

var (
	accent   = lipgloss.Color("147")
	dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	faint    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bold     = lipgloss.NewStyle().Bold(true)
	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	okStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	accented = lipgloss.NewStyle().Foreground(accent)
	inputBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	spinner  = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	// Captures the mouse: a click opens a step and a drag selects and copies text (selection.go).
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "loop"
	if m.width == 0 {
		return v
	}
	rows := m.screenRows()
	if m.drag != nil {
		highlightRows(rows, *m.drag, m.width)
	}
	v.Content = strings.Join(rows, "\n")
	return v
}

// screenRows lays out the whole screen, one string per row: the scrolled transcript, the command
// or picker panel, the input box and the status line.
func (m *Model) screenRows() []string {
	lines, _ := m.transcript()
	panel := m.panel()
	input := inputBox.Width(m.width).Render(m.input.View())
	status := m.statusLine()

	height := m.transcriptHeight()
	scroll := min(m.scroll, max(len(lines)-height, 0))
	start := max(len(lines)-height-scroll, 0)
	visible := slices.Clone(lines[start:min(start+height, len(lines))])
	for len(visible) < height {
		visible = append(visible, "")
	}

	rows := slices.Concat(visible, panel)
	for i, row := range rows {
		rows[i] = ansi.Truncate(row, m.width, "…")
	}
	rows = append(rows, "")
	rows = append(rows, strings.Split(input, "\n")...)
	return append(rows, ansi.Truncate(status, m.width, "…"), "")
}

// transcriptHeight is how many rows are left for the conversation after the panel, input and status line.
func (m *Model) transcriptHeight() int {
	const spacing = 2 // a blank line above the input box and one below the status line
	return max(m.height-len(m.panel())-(m.input.Height()+2)-1-spacing, 1)
}

// transcript renders the whole conversation as lines.
func (m *Model) transcript() (lines []string, starts []int) {
	width := max(m.width-4, 10)
	lines = m.welcome()
	for i, e := range m.entries {
		if i == 0 || !(isStep(e) && isStep(m.entries[i-1])) {
			lines = append(lines, "")
		}
		starts = append(starts, len(lines))
		lines = append(lines, m.entryLines(i, width)...)
	}
	return append(lines, m.liveLines(width)...), starts
}

// quietGap is how long a running turn can go without visible progress before "Working" appears.
const quietGap = 400 * time.Millisecond

// liveLines shows what the running turn is doing right now, below the transcript: what the model
// is thinking about while it thinks, or "Working" whenever nothing else on screen is moving, so a
// turn never looks done before it is.
func (m *Model) liveLines(width int) []string {
	if !m.running {
		return nil
	}
	if t := m.thinking; t != nil && !t.done {
		return []string{"", "  " + m.thoughtLine(t, width)}
	}
	toolRunning := slices.ContainsFunc(m.entries, func(e entry) bool { return e.tool != nil && !e.tool.done })
	idle := m.now().Sub(m.lastActivity)
	if toolRunning || idle < quietGap {
		return nil
	}
	return []string{"", "  " + accented.Render(spinner[m.frame%len(spinner)]) + " " + shimmer("Working", m.frame) +
		faint.Render(" · "+formatSeconds(idle))}
}

// entryLines draws one entry, reusing the last drawing while nothing it depends on has changed.
// Only a step still running is redrawn every frame, so long sessions stay cheap to render.
func (m *Model) entryLines(i, width int) []string {
	e := &m.entries[i]
	running := (e.tool != nil && !e.tool.done) || (e.thought != nil && !e.thought.done)
	key := fmt.Sprintf("%d %t %t %d", width, e.expanded, i == m.selected, len(e.text))
	if !running && e.rendered != nil && e.renderedFor == key {
		return e.rendered
	}
	lines := m.drawEntry(i, width)
	if !running {
		e.rendered, e.renderedFor = lines, key
	}
	return lines
}

func (m *Model) drawEntry(i, width int) []string {
	e := m.entries[i]
	var lines []string
	switch e.kind {
	case userEntry:
		for j, l := range strings.Split(lipgloss.Wrap(e.text, width, ""), "\n") {
			prefix := "  "
			if j == 0 {
				prefix = accented.Render("› ")
			}
			lines = append(lines, prefix+l)
		}
	case assistantEntry:
		for _, l := range strings.Split(m.md.render(e.text, width), "\n") {
			lines = append(lines, "  "+l)
		}
	case toolEntry:
		lines = append(lines, m.stepPrefix(i)+m.toolLine(e.tool, width))
		if e.tool.done && e.expanded {
			lines = append(lines, detailLines(toolDetail(e.tool, width-4))...)
		}
	case thinkingEntry:
		lines = append(lines, m.stepPrefix(i)+m.thoughtLine(e.thought, width))
		if e.expanded {
			lines = append(lines, detailLines(thoughtDetail(e.thought, width-4))...)
		}
	case noticeEntry:
		for _, l := range strings.Split(lipgloss.Wrap(e.text, width, ""), "\n") {
			lines = append(lines, "  "+dim.Render(l))
		}
	case errorEntry:
		for _, l := range strings.Split(lipgloss.Wrap(e.text, width, ""), "\n") {
			lines = append(lines, "  "+errStyle.Render(l))
		}
	}
	return lines
}

// detailLines indents an opened step's contents under it with a rule down the left.
func detailLines(detail []string) []string {
	lines := make([]string, len(detail))
	for i, l := range detail {
		lines[i] = "    " + faint.Render("│ ") + dim.Render(l)
	}
	return lines
}

// stepPrefix marks the highlighted step so it stands out from its neighbours.
func (m *Model) stepPrefix(i int) string {
	if i == m.selected {
		return accented.Render("▸ ")
	}
	return "  "
}

// isStep reports whether an entry sits flush with its neighbours, like tool calls and thinking.
func isStep(e entry) bool {
	return e.kind == toolEntry || e.kind == thinkingEntry
}

// thoughtLine shows what the model is working on and for how long, or how long it thought once done.
func (m *Model) thoughtLine(t *thought, width int) string {
	heading := latestHeading(t.summary)
	if t.done {
		line := dim.Render("◦ Thought for " + formatSeconds(t.duration))
		if heading != "" {
			line += faint.Render(" · " + truncateText(heading, width-24))
		}
		return line
	}
	label := heading
	switch {
	case label != "":
	case t.responded:
		label = "Thinking"
	default:
		label = "Waiting for the model"
	}
	return accented.Render(spinner[m.frame%len(spinner)]) + " " + shimmer(truncateText(label, width-14), m.frame) +
		faint.Render(" · "+formatSeconds(m.now().Sub(t.started)))
}

// thoughtDetail is the full reasoning summary, wrapped, or a note when the model shared none.
func thoughtDetail(t *thought, width int) []string {
	switch {
	case t.summary != "":
		return strings.Split(lipgloss.Wrap(strings.ReplaceAll(t.summary, "**", ""), width, ""), "\n")
	case t.done:
		return []string{"The model didn't share its reasoning for this step."}
	default:
		return []string{"No reasoning shared yet."}
	}
}

// latestHeading is the bold title of the newest reasoning section, like "**Reading the config**".
func latestHeading(summary string) string {
	lines := strings.Split(summary, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if len(l) > 4 && strings.HasPrefix(l, "**") && strings.HasSuffix(l, "**") && strings.Count(l, "**") == 2 {
			return strings.Trim(l, "*")
		}
	}
	return ""
}

func formatSeconds(d time.Duration) string {
	return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
}

func (m *Model) welcome() []string {
	return []string{
		"",
		"  " + bold.Render("loop") + dim.Render("  "+shortPath(m.deps.Dir)),
		"  " + faint.Render("/login to sign in · /model to pick a model · /help for more"),
	}
}

// shortPath writes a path inside the home directory starting from ~.
func shortPath(dir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return dir
}

func (m *Model) toolLine(t *toolCall, width int) string {
	label := t.name
	if t.summary != "" {
		label = toolVerb(t) + " " + firstLine(t.summary)
	}
	label = truncateText(label, width-14)
	switch {
	case !t.done:
		return accented.Render(spinner[m.frame%len(spinner)]) + " " + shimmer(label, m.frame)
	case t.err != nil:
		return errStyle.Render("✗") + " " + dim.Render(label) + faint.Render(" · "+truncateText(firstLine(t.err.Error()), 40))
	case t.restored:
		return okStyle.Render("✓") + " " + dim.Render(label)
	default:
		return okStyle.Render("✓") + " " + dim.Render(label) + faint.Render(" · "+formatDuration(t.duration))
	}
}

func toolVerb(t *toolCall) string {
	if t.name != "bash" {
		return t.name
	}
	if t.done {
		return "Ran"
	}
	return "Running"
}

func toolDetail(t *toolCall, width int) []string {
	var lines []string
	if t.thought != nil {
		lines = append(lines, "Thought for "+formatSeconds(t.thought.duration))
		lines = append(lines, thoughtDetail(t.thought, width)...)
		lines = append(lines, "")
	}
	var out []string
	if t.name == "bash" {
		out = append(out, "$ "+t.summary)
	}
	output := strings.TrimRight(t.output, "\n")
	switch {
	case output != "":
		for _, l := range strings.Split(output, "\n") {
			out = append(out, truncateText(expandTabs(l), width))
		}
	case t.err == nil:
		out = append(out, "(no output)")
	}
	if t.err != nil {
		out = append(out, "error: "+t.err.Error())
	}
	if len(out) > maxDetailLines {
		hidden := len(out) - maxDetailLines
		out = append(out[:maxDetailLines], fmt.Sprintf("… %d more lines", hidden))
	}
	return append(lines, out...)
}

// panel is the command suggestions or the picker shown above the input, if either is open.
func (m *Model) panel() []string {
	if m.picker != nil {
		return m.picker.lines()
	}
	matches := m.suggestions()
	if len(matches) == 0 {
		return nil
	}
	lines := []string{""}
	for i, c := range matches {
		name := fmt.Sprintf("/%-8s", c.name)
		if i == m.suggestion {
			lines = append(lines, "  "+accented.Render("› "+name)+" "+dim.Render(c.description))
		} else {
			lines = append(lines, "    "+dim.Render(name)+" "+faint.Render(c.description))
		}
	}
	return lines
}

func (m *Model) statusLine() string {
	var left []string
	switch {
	case m.deps.mock():
		left = append(left, "mock")
	case m.email == "":
		left = append(left, "not signed in")
	default:
		model := m.modelName
		if model == "" {
			model = "no model yet"
		}
		left = append(left, model, m.email)
	}
	right := "click a step to open it · /help"
	if m.selected >= 0 {
		right = "enter open/close · alt+↑↓ move · esc done"
	}
	if m.running {
		right = accented.Render(spinner[m.frame%len(spinner)]) + dim.Render(" working · esc to stop")
	}
	if m.copied != "" {
		right = okStyle.Render("✓") + dim.Render(fmt.Sprintf(" copied %d characters", len([]rune(m.copied))))
	}
	leftText := faint.Render(" " + strings.Join(left, " · "))
	gap := max(m.width-lipgloss.Width(leftText)-lipgloss.Width(right)-1, 1)
	return leftText + strings.Repeat(" ", gap) + faint.Render(right)
}

// shimmer renders text in grey with a bright band sweeping across it, to show work in progress.
func shimmer(text string, frame int) string {
	runes := []rune(text)
	const band = 4
	center := frame%(len(runes)+band*4) - band*2
	var b strings.Builder
	for i, r := range runes {
		distance := max(i-center, center-i)
		level := 244
		if distance < band {
			level = 255 - distance*3
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprint(level))).Render(string(r)))
	}
	return b.String()
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func truncateText(s string, width int) string {
	if width <= 1 || lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > width-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
