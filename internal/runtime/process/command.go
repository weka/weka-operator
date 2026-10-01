// Package process runs subprocesses with unified logging, output capture, and lifecycle
// ownership: every launch gets its own process group, is waited on exactly once, and is
// terminated with SIGTERM (never SIGKILL) on cancellation.
package process

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// OutputMode selects how a command's stdout and stderr are wired.
type OutputMode int

const (
	Capture OutputMode = iota // buffer stdout/stderr into the Result
	Inherit                   // pass through to this process's own stdout/stderr
)

// LogPolicy selects which parts of a command's lifecycle are logged.
type LogPolicy int

const (
	LogAll       LogPolicy = iota // execution and captured output (default)
	LogExecution                  // execution only; captured output not logged
	LogOutput                     // captured output only; execution not logged
	LogNone                       // nothing logged
)

// Command describes one subprocess invocation.
type Command struct {
	Path   string
	Args   []string
	Stdin  io.Reader
	Env    []string
	Dir    string
	Output OutputMode
	Log    LogPolicy
}

// Result holds a finished command's captured output and exit status.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CommandRunner executes a Command to completion.
type CommandRunner interface {
	Run(ctx context.Context, c Command) (Result, error)
}

// Failure classifies why a Command did not complete successfully.
type Failure int

const (
	FailureLaunch    Failure = iota // the executable could not be started
	FailureExit                     // the process started and exited non-zero (or via signal)
	FailureCancelled                // the caller's context was cancelled
)

// ExecError reports a failed command while retaining whatever output was captured.
type ExecError struct {
	Cmd    string
	Kind   Failure
	Result Result
	Err    error
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
}

func (e *ExecError) Unwrap() error { return e.Err }

// Shell wraps script in a fail-fast `sh -c "set -e\n"+script` command. It is the only
// fail-fast path in this package; command wrappers that build Command directly do not go
// through it.
func Shell(script string) Command {
	return Command{Path: "sh", Args: []string{"-c", "set -e\n" + script}}
}

// ShellQuote returns s as one single-quoted shell word, escaping embedded single quotes, for
// interpolating data into a Shell script.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// render returns the text used for logging and ExecError.Cmd. Under LogOutput and LogNone,
// args are suppressed to the executable name only, so secrets passed as arguments never reach
// logs or error text. Takes *Command since Command is large enough that gocritic flags pass-by-value.
func render(c *Command) string {
	switch c.Log {
	case LogOutput, LogNone:
		return c.Path
	default:
		if len(c.Args) == 0 {
			return c.Path
		}
		return c.Path + " " + strings.Join(c.Args, " ")
	}
}
