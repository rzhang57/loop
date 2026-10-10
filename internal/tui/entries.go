package tui

import "time"

type entryKind int

const (
	userEntry entryKind = iota
	assistantEntry
	toolEntry
	noticeEntry
	errorEntry
	thinkingEntry
)

type entry struct {
	kind     entryKind
	text     string
	tool     *toolCall
	thought  *thought
	expanded bool

	// rendered caches the entry's lines; renderedFor says what they were drawn for.
	rendered    []string
	renderedFor string
}

type toolCall struct {
	id       string
	name     string
	summary  string
	output   string
	err      error
	done     bool
	duration time.Duration
	// restored means the call came from a saved conversation, which does not keep how long it took.
	restored bool
	// thought is the model's reasoning that led to this call, shown when the call is opened.
	thought *thought
}

// thought is one stretch of the model working before it writes or calls a tool.
type thought struct {
	started   time.Time
	responded bool
	summary   string
	done      bool
	duration  time.Duration
}
