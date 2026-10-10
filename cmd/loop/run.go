// The "loop run" command: one prompt, no interface.
// runHeadless reads the flags and prompt, checks the sign-in, and has a headless.Runner take the
// prompt to completion with the same agent the chat uses, then printReport writes the JSON report
// to stdout. A failed run exits non-zero after the report is printed.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/rzhang57/loop/internal/auth"
	"github.com/rzhang57/loop/internal/headless"
	"github.com/rzhang57/loop/internal/store"
)

// runHeadless runs one prompt to completion and prints the report as JSON, exiting non-zero if it failed.
func runHeadless(s *store.Store, authClient *auth.Client, dir string, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	model := flags.String("model", "", "model slug, defaults to the one last chosen")
	sessionID := flags.String("session", "", "continue this session instead of starting a new one")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		fmt.Print(usage)
		return nil
	} else if err != nil {
		return printReport(headless.Report{Error: err.Error()})
	}
	prompt, err := promptFrom(flags, flags.Args())
	if err != nil {
		return printReport(headless.Report{Error: err.Error()})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if _, err := authClient.Current(ctx); err != nil {
		return printReport(headless.Report{Error: err.Error() + ", run: loop login"})
	}
	if *model == "" && *sessionID == "" {
		*model, _, _ = s.Get(ctx, "model")
	}

	runner := headless.Runner{Agent: newAgent(newOpenAI(authClient), dir), Store: s, Dir: dir, Now: time.Now}
	return printReport(runner.Run(ctx, *sessionID, *model, prompt))
}

// promptFrom joins the words after the flags into a prompt, rejecting known flags placed after it,
// which the flag package would otherwise fold into the prompt.
func promptFrom(flags *flag.FlagSet, words []string) (string, error) {
	for _, w := range words {
		name, _, _ := strings.Cut(strings.TrimLeft(w, "-"), "=")
		if strings.HasPrefix(w, "-") && flags.Lookup(name) != nil {
			return "", fmt.Errorf("put flags before the prompt: loop run %s \"...\"", w)
		}
	}
	prompt := strings.TrimSpace(strings.Join(words, " "))
	if prompt == "" {
		return "", errors.New(`run needs a prompt: loop run "what's in this folder?"`)
	}
	return prompt, nil
}

// printReport writes the report as JSON on stdout, and fails if the run did.
func printReport(report headless.Report) error {
	if report.Steps == nil {
		report.Steps = []headless.Step{}
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	if err := out.Encode(report); err != nil {
		return err
	}
	if report.Error != "" {
		fmt.Fprintln(os.Stderr, "loop:", report.Error)
		return errReported
	}
	return nil
}
