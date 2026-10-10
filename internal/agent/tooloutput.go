// How a failed tool call is written into the history and read back.
// failedToolOutput marks the output of a call that failed, and ToolError separates such an output
// into what the tool printed and the error, so a saved conversation can show which calls failed
// when it is reopened.

package agent

import (
	"strings"

	"github.com/rzhang57/loop/internal/llm"
)

// errorSeparator comes between what a failed tool printed and its error.
const errorSeparator = "\nerror: "

// failedToolOutput is the output of a call that failed, marked as failed.
func failedToolOutput(callID, output string) llm.Item {
	item := llm.ToolOutput(callID, output)
	item.Failed = true
	return item
}

// ToolError separates a tool output from the history into what the tool printed and, when the
// call failed, its error.
func ToolError(item llm.Item) (text, errText string, failed bool) {
	output := item.Output
	if !item.Failed {
		return output, "", false
	}
	if i := strings.LastIndex(output, errorSeparator); i >= 0 {
		return output[:i], output[i+len(errorSeparator):], true
	}
	return "", output, true
}
