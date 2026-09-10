package wekacontainer

import (
	"fmt"
	"testing"

	"github.com/pkg/errors"
	clientexec "k8s.io/client-go/util/exec"
	utilexec "k8s.io/utils/exec"
)

// The drivers rebuild trigger keys off a non-zero exit of the weka CLI, distinguishing it from a
// failure of the exec plumbing. That distinction spans three packages -- remotecommand returns a
// client-go CodeExitError, podexec wraps it, and the caller wraps again with %w -- so assert the
// chain a rebuild depends on, and that a transport failure does not look like one.
func TestRebuildTriggerMatchesOnlyRemoteExit(t *testing.T) {
	wrapLikeProduction := func(inner error) error {
		wrapped := errors.Wrap(inner, "command DownloadDrivers failed") // podexec.go
		return fmt.Errorf("error downloading drivers: %w, stderr: %s", wrapped, "some stderr")
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "weka CLI exited non-zero -> rebuild",
			err:  wrapLikeProduction(clientexec.CodeExitError{Err: fmt.Errorf("command terminated with exit code 1"), Code: 1}),
			want: true,
		},
		{
			name: "CLI missing, exit 127 -> still a real remote exit",
			err:  wrapLikeProduction(clientexec.CodeExitError{Err: fmt.Errorf("command terminated with exit code 127"), Code: 127}),
			want: true,
		},
		{
			name: "exec stream broke -> not a rebuild signal",
			err:  wrapLikeProduction(fmt.Errorf("error dialing backend: connection refused")),
			want: false,
		},
		{
			name: "target pod unreachable (502) -> not a rebuild signal",
			err:  errors.Wrap(fmt.Errorf("Internal error occurred: error executing command in container"), "Exec failed to stream"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var exitErr utilexec.ExitError
			if got := errors.As(tt.err, &exitErr); got != tt.want {
				t.Fatalf("errors.As = %v, want %v (err: %v)", got, tt.want, tt.err)
			}
		})
	}
}
