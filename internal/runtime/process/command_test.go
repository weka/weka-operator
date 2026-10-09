package process

import (
	"context"
	"errors"
	"testing"
)

func TestShellIsFailFast(t *testing.T) {
	c := Shell("echo hi")
	if c.Path != "sh" {
		t.Fatalf("Path = %q, want sh", c.Path)
	}
	want := []string{"-c", "set -e\necho hi"}
	if len(c.Args) != 2 || c.Args[0] != want[0] || c.Args[1] != want[1] {
		t.Fatalf("Args = %v, want %v", c.Args, want)
	}
}

func TestRenderSuppressesArgsForOutputAndNone(t *testing.T) {
	c := Command{Path: "tool", Args: []string{"--arg", "hidden-value"}, Log: LogOutput}
	if got := render(&c); got != "tool" {
		t.Fatalf("LogOutput render = %q, want %q (args must not leak)", got, "tool")
	}
	c.Log = LogNone
	if got := render(&c); got != "tool" {
		t.Fatalf("LogNone render = %q, want %q (args must not leak)", got, "tool")
	}
	c.Log = LogAll
	if got := render(&c); got != "tool --arg hidden-value" {
		t.Fatalf("LogAll render = %q, want full command", got)
	}
	c.Log = LogExecution
	if got := render(&c); got != "tool --arg hidden-value" {
		t.Fatalf("LogExecution render = %q, want full command", got)
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	if got, want := ShellQuote(`{"k":"it's"}`), `'{"k":"it'\''s"}'`; got != want {
		t.Fatalf("ShellQuote = %s, want %s", got, want)
	}
}

func TestExecErrorUnwrapsCancellation(t *testing.T) {
	err := &ExecError{Cmd: "sleep", Kind: FailureCancelled, Err: context.Canceled}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("errors.Is(err, context.Canceled) = false, want true")
	}
	if err.Error() == "" {
		t.Fatal("Error() must not be empty")
	}
}

func TestRenderShellShowsScriptOnly(t *testing.T) {
	c := Shell("weka local ps\necho done")
	if got := render(&c); got != "weka local ps\necho done" {
		t.Fatalf("render = %q", got)
	}
	c.Log = LogNone
	if got := render(&c); got != "sh" {
		t.Fatalf("LogNone render = %q, want %q", got, "sh")
	}
}
