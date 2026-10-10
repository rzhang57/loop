package main

import (
	"flag"
	"testing"

	"github.com/rzhang57/loop/internal/tui"
)

func TestPromptFrom(t *testing.T) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.String("model", "", "")
	flags.String("session", "", "")

	if p, err := promptFrom(flags, []string{"what's", "here?"}); err != nil || p != "what's here?" {
		t.Fatalf("got %q %v", p, err)
	}
	if p, err := promptFrom(flags, []string{"subtract", "-5", "--", "dashes", "-x"}); err != nil || p != "subtract -5 -- dashes -x" {
		t.Fatalf("words that only look like flags belong to the prompt: %q %v", p, err)
	}
	for _, words := range [][]string{nil, {"  "}, {"hi", "--model", "x"}, {"hi", "-model", "x"}, {"hi", "--session=abc"}} {
		if _, err := promptFrom(flags, words); err == nil {
			t.Errorf("%q should be rejected", words)
		}
	}
}

func TestResumeFrom(t *testing.T) {
	var picking tui.Deps
	if err := resumeFrom(&picking, nil); err != nil || !picking.PickSession || picking.ResumeID != "" {
		t.Fatalf("no ID should open the list: %+v %v", picking, err)
	}
	var direct tui.Deps
	if err := resumeFrom(&direct, []string{"20261008-120000-abcdef"}); err != nil || direct.PickSession || direct.ResumeID != "20261008-120000-abcdef" {
		t.Fatalf("an ID should open that conversation: %+v %v", direct, err)
	}
	if err := resumeFrom(&tui.Deps{}, []string{"a", "b"}); err == nil {
		t.Fatal("two IDs should be rejected")
	}
}
