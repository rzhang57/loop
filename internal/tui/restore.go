// Rebuilding the transcript of a saved conversation.
// restoredEntries turns saved llm.Items back into entries: the user's messages, the model's
// answers, and each tool call as a finished one-line step holding its output, marked failed when
// agent.ToolError finds it failed. Reasoning and timings are not saved, so restored steps show
// neither. Called by resume.go when a conversation is resumed.

package tui

import (
	"errors"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
)

// restoredEntries draws a saved conversation as entries, using summarize to describe each tool call.
func restoredEntries(items []llm.Item, summarize func(name, arguments string) string) []entry {
	var entries []entry
	calls := map[string]*toolCall{}
	for _, item := range items {
		switch {
		case item.Type == "message" && item.Role == "user":
			entries = append(entries, entry{kind: userEntry, text: item.Content})
		case item.Type == "message" && item.Content != "":
			entries = append(entries, entry{kind: assistantEntry, text: item.Content})
		case item.IsToolCall():
			call := &toolCall{id: item.CallID, name: item.Name, summary: summarize(item.Name, item.Arguments), done: true, restored: true}
			calls[item.CallID] = call
			entries = append(entries, entry{kind: toolEntry, tool: call})
		case item.Type == "function_call_output":
			call := calls[item.CallID]
			if call == nil {
				continue
			}
			text, errText, failed := agent.ToolError(item)
			call.output = text
			if failed {
				call.err = errors.New(errText)
			}
		}
	}
	return entries
}
