// Package headless runs one prompt to completion without the interface, for `loop run`.
// Runner.Run loads or starts a session, calls agent.Agent.Send, and folds the streamed events into a
// Report: ordered steps of text, reasoning and tool calls, with turn count, token usage and timing.
// The conversation is saved back through session so a later run can continue it. Called by
// cmd/loop, which prints the Report as JSON.
package headless

import (
	"context"
	"strings"
	"time"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/session"
	"github.com/rzhang57/loop/internal/store"
)

// Report is everything one headless run did, in the order it happened.
type Report struct {
	Session    string    `json:"session"`
	Model      string    `json:"model"`
	Text       string    `json:"text"`
	Steps      []Step    `json:"steps"`
	Turns      int       `json:"turns"`
	Usage      llm.Usage `json:"usage"`
	DurationMS int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// Step is some text the model wrote, a summary of its reasoning, or one tool call it made.
type Step struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Runner runs one prompt to completion and reports every step.
type Runner struct {
	Agent *agent.Agent
	Store *store.Store
	// Dir is the working directory, recorded with the session.
	Dir string
	Now func() time.Time
}

// Run continues sessionID, or starts a new session when it is empty, and reports what happened.
func (r Runner) Run(ctx context.Context, sessionID, model, prompt string) Report {
	start := r.Now()
	report := Report{Session: sessionID, Model: model, Steps: []Step{}}
	if report.Session == "" {
		report.Session = session.NewID(start)
	} else {
		sess, ok, err := session.Load(ctx, r.Store, sessionID)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		if !ok {
			report.Error = "no session " + sessionID
			return report
		}
		r.Agent.Restore(sess.Items)
		if model == "" {
			report.Model = sess.Model
		}
	}
	if report.Model == "" {
		report.Error = "no model chosen, pass --model"
		return report
	}

	steps := map[string]int{}
	var finalText strings.Builder
	err := r.Agent.Send(ctx, report.Model, prompt, func(e agent.Event) {
		switch e := e.(type) {
		case agent.TextDelta:
			if n := len(report.Steps); n > 0 && report.Steps[n-1].Type == "text" {
				report.Steps[n-1].Text += e.Text
			} else {
				report.Steps = append(report.Steps, Step{Type: "text", Text: e.Text})
			}
			finalText.WriteString(e.Text)
		case agent.ReasoningDelta:
			n := len(report.Steps)
			if n == 0 || report.Steps[n-1].Type != "reasoning" {
				report.Steps = append(report.Steps, Step{Type: "reasoning"})
				n++
			}
			step := &report.Steps[n-1]
			if e.NewPart && step.Text != "" {
				step.Text += "\n\n"
			}
			step.Text += e.Text
		case agent.ToolStarted:
			steps[e.CallID] = len(report.Steps)
			report.Steps = append(report.Steps, Step{Type: "tool", Tool: e.Name, Arguments: e.Arguments})
			finalText.Reset()
		case agent.ToolFinished:
			step := &report.Steps[steps[e.CallID]]
			step.Output, step.DurationMS = e.Output, e.Duration.Milliseconds()
			if e.Err != nil {
				step.Error = e.Err.Error()
			}
		case agent.UsageReported:
			report.Turns++
			report.Usage = report.Usage.Add(e.Usage)
		}
	})
	report.Text = finalText.String()
	if err != nil {
		report.Error = err.Error()
	}
	saveErr := session.Save(context.WithoutCancel(ctx), r.Store, session.Session{
		ID: report.Session, Model: report.Model, Dir: r.Dir, Updated: r.Now(), Items: r.Agent.History(),
	})
	if saveErr != nil && report.Error == "" {
		report.Error = "save session: " + saveErr.Error()
	}
	report.DurationMS = r.Now().Sub(start).Milliseconds()
	return report
}
