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
	c := Command{Path: "aws", Args: []string{"--secret", "s3kr3t"}, Log: LogOutput}
	if got := render(&c); got != "aws" {
		t.Fatalf("LogOutput render = %q, want %q (args must not leak)", got, "aws")
	}
	c.Log = LogNone
	if got := render(&c); got != "aws" {
		t.Fatalf("LogNone render = %q, want %q (args must not leak)", got, "aws")
	}
	c.Log = LogAll
	if got := render(&c); got != "aws --secret s3kr3t" {
		t.Fatalf("LogAll render = %q, want full command", got)
	}
	c.Log = LogExecution
	if got := render(&c); got != "aws --secret s3kr3t" {
		t.Fatalf("LogExecution render = %q, want full command", got)
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
