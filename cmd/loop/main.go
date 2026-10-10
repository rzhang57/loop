// Command loop is the harness binary: a terminal chat where an agent works with a bash tool.
// This file dispatches on the first argument: with none it opens the tui chat, "resume" (or
// "--resume") opens it on a saved conversation or the list of them, "--mock" opens it against
// mock's scripted model, "run" executes one prompt headlessly (run.go), and "login", "logout" and
// "status" manage the ChatGPT sign-in (account.go). wiring.go builds the agent, its tools, its
// instructions and the OpenAI client that every mode shares. Sign-in, settings and sessions live
// in the store database.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	tea "charm.land/bubbletea/v2"

	"github.com/rzhang57/loop/internal/auth"
	"github.com/rzhang57/loop/internal/store"
	"github.com/rzhang57/loop/internal/tui"
)

const usage = `usage: loop [command]

With no command, opens the chat in this directory.

commands:
  resume   reopen a saved conversation in the chat, picking from a list without an ID
           loop resume [ID]   (or loop --resume [ID])
  run      run one prompt without the interface and print a JSON report
           loop run [--model SLUG] [--session ID] "prompt"
  login    sign in with your ChatGPT account
  logout   sign out and revoke the saved session
  status   show who is signed in
  --mock   open the chat with a scripted model and pretend tools

environment:
  LOOP_HOME              where sign-in, settings and sessions are kept (default ~/.loop)
  LOOP_OPENAI_BASE_URL   send model requests somewhere other than api.openai.com
`

// errReported means the failure is already in the printed output, so main only sets the exit code.
var errReported = errors.New("reported")

func main() {
	err := run(os.Args[1:])
	if err != nil && !errors.Is(err, errReported) {
		fmt.Fprintln(os.Stderr, "loop:", err)
	}
	if err != nil {
		os.Exit(1)
	}
}

func run(args []string) error {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if command == "--mock" {
		return chat(mockDeps(), dir)
	}

	path, err := store.DefaultPath()
	if err != nil {
		return err
	}
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	authClient := auth.New(s, auth.DefaultConfig())
	switch command {
	case "":
		return chat(realDeps(s, authClient, dir), dir)
	case "resume", "--resume":
		deps := realDeps(s, authClient, dir)
		if err := resumeFrom(&deps, args[1:]); err != nil {
			return err
		}
		return chat(deps, dir)
	case "run":
		return runHeadless(s, authClient, dir, args[1:])
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch command {
	case "login":
		return login(ctx, authClient)
	case "logout":
		return logout(ctx, authClient)
	case "status":
		return status(ctx, authClient)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// resumeFrom has the chat open the saved conversation named in args, or the list of them when
// args is empty.
func resumeFrom(deps *tui.Deps, args []string) error {
	switch len(args) {
	case 0:
		deps.PickSession = true
	case 1:
		deps.ResumeID = args[0]
	default:
		return errors.New("resume takes at most one session ID: loop resume [ID]")
	}
	return nil
}

// chat opens the interface for dir.
func chat(deps tui.Deps, dir string) error {
	deps.Dir = dir
	_, err := tea.NewProgram(tui.New(deps)).Run()
	return err
}
