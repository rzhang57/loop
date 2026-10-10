// Package llm defines the provider-neutral conversation types and the OpenAI client behind them.
// llm.go holds the shared vocabulary: Item (messages, tool calls and tool outputs, with their
// exact JSON wire shape), ToolSpec and Namespace, Request and Response, Usage, and the
// streaming Delta kinds. The Model interface is the one seam the rest of the code depends on:
// agent calls Respond each turn, and openai.go and mock provide implementations.
package llm

import (
	"context"
	"encoding/json"
)

// Item is one entry in a conversation: a message, a tool call, or a tool call's output.
type Item struct {
	Type      string
	Role      string
	Content   string
	CallID    string
	Name      string
	Namespace string
	Arguments string
	Output    string
	// Failed marks the output of a tool call that failed. It is saved with a session but never sent.
	Failed bool
}

// MarshalJSON writes exactly the fields each item type needs, keeping required ones even when empty.
func (i Item) MarshalJSON() ([]byte, error) {
	switch i.Type {
	case "function_call":
		call := map[string]string{"type": i.Type, "call_id": i.CallID, "name": i.Name, "arguments": i.Arguments}
		if i.Namespace != "" {
			call["namespace"] = i.Namespace
		}
		return json.Marshal(call)
	case "function_call_output":
		return json.Marshal(map[string]string{"type": i.Type, "call_id": i.CallID, "output": i.Output})
	default:
		return json.Marshal(map[string]string{"type": i.Type, "role": i.Role, "content": i.Content})
	}
}

func UserMessage(text string) Item {
	return Item{Type: "message", Role: "user", Content: text}
}

func AssistantMessage(text string) Item {
	return Item{Type: "message", Role: "assistant", Content: text}
}

func ToolOutput(callID, output string) Item {
	return Item{Type: "function_call_output", CallID: callID, Output: output}
}

func (i Item) IsToolCall() bool {
	return i.Type == "function_call"
}

// ToolSpec describes a tool the model may call, with its arguments as a JSON schema.
type ToolSpec struct {
	Namespace   Namespace
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Namespace is a named group of tools; plan usage only accepts tools inside one.
type Namespace struct {
	Name        string
	Description string
}

type Request struct {
	Model        string
	Instructions string
	Input        []Item
	Tools        []ToolSpec
}

// Response holds what the model produced in one turn, in order, and the tokens it used.
type Response struct {
	Items []Item
	Usage Usage
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	CachedTokens int `json:"cached_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		CachedTokens: u.CachedTokens + other.CachedTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
	}
}

func (r Response) ToolCalls() []Item {
	var calls []Item
	for _, item := range r.Items {
		if item.IsToolCall() {
			calls = append(calls, item)
		}
	}
	return calls
}

// Model streams one turn of a conversation, calling onDelta as each piece arrives.
type Model interface {
	Respond(ctx context.Context, req Request, onDelta func(Delta)) (Response, error)
}

// Delta is one piece of a streaming response.
type Delta struct {
	Kind DeltaKind
	Text string
}

type DeltaKind int

const (
	// ResponseStarted means the server accepted the request and the model began working.
	ResponseStarted DeltaKind = iota
	OutputText
	ReasoningText
	// ReasoningPart starts a new section of the reasoning summary.
	ReasoningPart
)

type ModelInfo struct {
	Slug        string
	DisplayName string
}
