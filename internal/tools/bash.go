// Package tools holds the agent tools that run on the local machine.
// Bash implements agent.Tool as the local.bash tool: it runs a command with bash -c in a fixed
// directory under a timeout, in its own process group so stopping it also kills its children.
// Output is captured by headTail, which keeps the start and end of long output and counts what it
// dropped. Registered with the agent by cmd/loop.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/rzhang57/loop/internal/agent"
	"github.com/rzhang57/loop/internal/llm"
)

const maxOutputBytes = 30_000

// Bash runs shell commands in a fixed directory, without asking for permission.
type Bash struct {
	Dir     string
	Timeout time.Duration
}

type bashArgs struct {
	Command string `json:"command"`
}

// Local groups tools that run on the user's machine.
var Local = llm.Namespace{Name: "local", Description: "Tools that run commands on the user's machine."}

func (b Bash) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Namespace:   Local,
		Name:        "bash",
		Description: fmt.Sprintf("Run a bash command in %s and return its combined stdout and stderr. Commands time out after %s.", b.Dir, b.Timeout),
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {"command": {"type": "string", "description": "The command to run."}},
			"required": ["command"],
			"additionalProperties": false
		}`),
	}
}

func (b Bash) Summary(args json.RawMessage) string {
	var a bashArgs
	json.Unmarshal(args, &a)
	return a.Command
}

func (b Bash) Run(ctx context.Context, args json.RawMessage) (agent.Result, error) {
	text, err := b.run(ctx, args)
	return agent.Result{Text: text}, err
}

func (b Bash) run(ctx context.Context, args json.RawMessage) (string, error) {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil || a.Command == "" {
		return "", errors.New("bash needs a non-empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()

	var output headTail
	cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
	cmd.Dir = b.Dir
	cmd.Stdout, cmd.Stderr = &output, &output
	// Own process group, so a timeout or stop also kills the command's children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// A shell mid-fork can miss the first group kill, so sweep the group again.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return output.String(), fmt.Errorf("timed out after %s", b.Timeout)
	}
	return output.String(), err
}

// headTail keeps the first and last maxOutputBytes/2 bytes written to it and counts the rest,
// since the start and end of long output are where commands usually say what happened.
type headTail struct {
	head, tail []byte
	dropped    int
}

func (h *headTail) Write(p []byte) (int, error) {
	half := maxOutputBytes / 2
	n := len(p)
	if room := half - len(h.head); room > 0 {
		take := min(room, len(p))
		h.head = append(h.head, p[:take]...)
		p = p[take:]
	}
	h.tail = append(h.tail, p...)
	if extra := len(h.tail) - half; extra > 0 {
		h.dropped += extra
		h.tail = append(h.tail[:0], h.tail[extra:]...)
	}
	return n, nil
}

func (h *headTail) String() string {
	if h.dropped == 0 {
		return string(h.head) + string(h.tail)
	}
	return string(h.head) + fmt.Sprintf("\n... %d bytes omitted ...\n", h.dropped) + string(h.tail)
}
