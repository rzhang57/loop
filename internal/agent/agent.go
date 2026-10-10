// Package agent runs the model-and-tools loop for one conversation.
// Agent.Send appends the user's message, asks the llm.Model for a turn, runs every tool call the
// model makes through the Tool interface, appends the outputs, and repeats until the model answers
// without calling a tool (or maxTurns is hit). Progress streams out as Event values (text and
// reasoning deltas, tool start and finish, usage) for the caller to render. Summary describes a
// past call for a reopened conversation. Driven by tui and headless; tools come from tools and
// mock. tooloutput.go marks a failed call's output and reads the error back out.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/rzhang57/loop/internal/llm"
)

const maxTurns = 50

type Tool interface {
	Spec() llm.ToolSpec
	// Summary describes a call in a few words for its one-line status.
	Summary(args json.RawMessage) string
	Run(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result is what a tool hands back to the model.
type Result struct {
	Text string
}

type Event interface{ event() }

type TextDelta struct{ Text string }

type ToolStarted struct {
	CallID    string
	Name      string
	Arguments string
	Summary   string
}

type ToolFinished struct {
	CallID   string
	Output   string
	Err      error
	Duration time.Duration
}

// UsageReported carries the tokens one model request used.
type UsageReported struct{ Usage llm.Usage }

// RequestSent means a request just went to the model and its reply is awaited.
type RequestSent struct{}

// ResponseStarted means the model accepted the request and began working on it.
type ResponseStarted struct{}

// ReasoningDelta is more of the model's reasoning summary; NewPart starts a new section.
type ReasoningDelta struct {
	Text    string
	NewPart bool
}

func (TextDelta) event()       {}
func (ToolStarted) event()     {}
func (ToolFinished) event()    {}
func (UsageReported) event()   {}
func (RequestSent) event()     {}
func (ResponseStarted) event() {}
func (ReasoningDelta) event()  {}

// Agent holds one conversation and runs the model and its tools until each request is answered.
type Agent struct {
	model        llm.Model
	tools        []Tool
	Instructions string
	history      []llm.Item
}

func New(model llm.Model, tools ...Tool) *Agent {
	return &Agent{model: model, tools: tools}
}

// Reset forgets the conversation.
func (a *Agent) Reset() {
	a.history = nil
}

// History returns the conversation so far, for saving.
func (a *Agent) History() []llm.Item {
	return slices.Clone(a.history)
}

// Restore replaces the conversation with a saved one.
func (a *Agent) Restore(items []llm.Item) {
	a.history = slices.Clone(items)
}

// Send adds the user's message and keeps calling the named model, running the tools it asks for,
// until it replies without calling one.
func (a *Agent) Send(ctx context.Context, model, text string, emit func(Event)) error {
	a.history = append(a.history, llm.UserMessage(text))
	specs := make([]llm.ToolSpec, len(a.tools))
	for i, t := range a.tools {
		specs[i] = t.Spec()
	}

	for range maxTurns {
		emit(RequestSent{})
		resp, err := a.model.Respond(ctx, llm.Request{
			Model:        model,
			Instructions: a.Instructions,
			Input:        a.history,
			Tools:        specs,
		}, func(d llm.Delta) {
			if e := deltaEvent(d); e != nil {
				emit(e)
			}
		})
		if err != nil {
			return err
		}
		emit(UsageReported{Usage: resp.Usage})
		a.history = append(a.history, resp.Items...)

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			return nil
		}
		// Every call gets an output, even after cancellation, or the next request would be rejected.
		for _, call := range calls {
			a.history = append(a.history, a.runTool(ctx, call, emit))
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("stopped after %d model turns without a final answer", maxTurns)
}

func deltaEvent(d llm.Delta) Event {
	switch d.Kind {
	case llm.ResponseStarted:
		return ResponseStarted{}
	case llm.ReasoningPart:
		return ReasoningDelta{NewPart: true}
	case llm.ReasoningText:
		return ReasoningDelta{Text: d.Text}
	case llm.OutputText:
		return TextDelta{Text: d.Text}
	default:
		return nil
	}
}

func (a *Agent) runTool(ctx context.Context, call llm.Item, emit func(Event)) llm.Item {
	args := json.RawMessage(call.Arguments)
	tool := a.tool(call.Name)
	if tool == nil {
		err := errors.New("unknown tool " + call.Name)
		emit(ToolStarted{CallID: call.CallID, Name: call.Name, Arguments: call.Arguments})
		emit(ToolFinished{CallID: call.CallID, Err: err})
		return failedToolOutput(call.CallID, err.Error())
	}

	emit(ToolStarted{CallID: call.CallID, Name: call.Name, Arguments: call.Arguments, Summary: tool.Summary(args)})
	if err := ctx.Err(); err != nil {
		emit(ToolFinished{CallID: call.CallID, Err: err})
		return failedToolOutput(call.CallID, "cancelled by the user")
	}
	start := time.Now()
	result, err := tool.Run(ctx, args)
	emit(ToolFinished{CallID: call.CallID, Output: result.Text, Err: err, Duration: time.Since(start)})
	if err != nil {
		return failedToolOutput(call.CallID, result.Text+errorSeparator+err.Error())
	}
	return llm.ToolOutput(call.CallID, result.Text)
}

// Summary describes a past call to the named tool in a few words, or returns "" for an unknown tool.
func (a *Agent) Summary(name, arguments string) string {
	if tool := a.tool(name); tool != nil {
		return tool.Summary(json.RawMessage(arguments))
	}
	return ""
}

func (a *Agent) tool(name string) Tool {
	for _, t := range a.tools {
		if t.Spec().Name == name {
			return t
		}
	}
	return nil
}
