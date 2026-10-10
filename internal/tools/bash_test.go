package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, b Bash, command string) (string, error) {
	t.Helper()
	args, _ := json.Marshal(bashArgs{Command: command})
	return b.run(context.Background(), args)
}

func TestBashRunsInDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "marker.txt"), nil, 0o644)
	b := Bash{Dir: dir, Timeout: 10 * time.Second}

	out, err := run(t, b, "ls; echo oops >&2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "marker.txt") || !strings.Contains(out, "oops") {
		t.Fatalf("output %q should include the listing and stderr", out)
	}
}

func TestBashReportsFailureWithOutput(t *testing.T) {
	out, err := run(t, Bash{Dir: t.TempDir(), Timeout: 10 * time.Second}, "echo before; exit 3")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(out, "before") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashTimeoutKillsChildProcesses(t *testing.T) {
	dir := t.TempDir()
	b := Bash{Dir: dir, Timeout: 300 * time.Millisecond}

	start := time.Now()
	_, err := run(t, b, "(sleep 2; touch survived) & sleep 5")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want a timeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not stop the command promptly")
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "survived")); err == nil {
		t.Fatal("a background child outlived the timeout")
	}
}

func TestBashKeepsHeadAndTailOfHugeOutput(t *testing.T) {
	out, err := run(t, Bash{Dir: t.TempDir(), Timeout: 30 * time.Second},
		"echo START; head -c 5000000 /dev/zero | tr '\\0' x; echo; echo END")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxOutputBytes+100 || !strings.HasPrefix(out, "START") || !strings.HasSuffix(strings.TrimSpace(out), "END") || !strings.Contains(out, "bytes omitted") {
		t.Fatalf("got %d bytes, start/end kept: %v/%v", len(out), strings.HasPrefix(out, "START"), strings.HasSuffix(strings.TrimSpace(out), "END"))
	}
}

func TestBashShortOutputIsUntouched(t *testing.T) {
	out, _ := run(t, Bash{Dir: t.TempDir(), Timeout: 10 * time.Second}, "printf short")
	if out != "short" {
		t.Fatalf("got %q", out)
	}
}

func TestBashRejectsEmptyCommand(t *testing.T) {
	if _, err := run(t, Bash{Dir: t.TempDir(), Timeout: time.Second}, ""); err == nil {
		t.Fatal("an empty command should be rejected")
	}
}

func TestBashStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	args, _ := json.Marshal(bashArgs{Command: "sleep 5"})

	start := time.Now()
	_, err := Bash{Dir: t.TempDir(), Timeout: time.Minute}.Run(ctx, args)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancel should stop the command promptly, got %v after %s", err, time.Since(start))
	}
}

func TestBashWithAlreadyCancelledContextDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args, _ := json.Marshal(bashArgs{Command: "echo hi"})
	if _, err := (Bash{Dir: t.TempDir(), Timeout: time.Minute}).Run(ctx, args); err == nil {
		t.Fatal("a cancelled run should report an error")
	}
}
