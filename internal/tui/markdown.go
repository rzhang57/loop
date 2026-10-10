package tui

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
)

// markdownStyle is glamour's dark style with every background and outer margin removed,
// so rendered text sits directly on the terminal's own background.
func markdownStyle() ansi.StyleConfig {
	s := styles.DarkStyleConfig
	none := uint(0)
	s.Document = ansi.StyleBlock{Margin: &none}
	s.Heading.BlockSuffix = ""
	s.H1 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}}
	s.H2 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}}
	s.H3 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}}
	s.Code = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: stringPtr("147")}}
	codeBlockIndent := uint(2)
	s.CodeBlock.Margin = &codeBlockIndent
	s.CodeBlock.Color = nil
	chroma := *s.CodeBlock.Chroma
	chroma.Background = ansi.StylePrimitive{}
	chroma.Error.BackgroundColor = nil
	s.CodeBlock.Chroma = &chroma
	return s
}

type markdown struct {
	width    int
	renderer *glamour.TermRenderer
}

// render formats markdown for the given width, falling back to the raw text if rendering fails.
func (m *markdown) render(text string, width int) string {
	if m.renderer == nil || m.width != width {
		r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle()), glamour.WithWordWrap(width))
		if err != nil {
			return text
		}
		m.renderer, m.width = r, width
	}
	out, err := m.renderer.Render(expandTabs(text))
	if err != nil {
		return text
	}
	return strings.Trim(out, "\n")
}

// expandTabs swaps tabs for spaces, since the terminal renderer drops tab characters.
func expandTabs(s string) string {
	return strings.ReplaceAll(s, "\t", "    ")
}

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
