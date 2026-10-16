package weka

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// EnsureManagedContainer creates the named container with setupArgs if it doesn't already exist.
// Starting it is a separate step, see StartManagedContainer.
// Mirrors the setup half of Python ensure_managed_local_container() at weka_runtime.py.
func EnsureManagedContainer(ctx context.Context, runner process.CommandRunner, name string, setupArgs ...string) error {
	found, err := FindLocalContainer(ctx, runner, name)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	args := append([]string{"local", "setup", name}, setupArgs...)
	if _, err := runner.Run(ctx, process.Command{Path: "weka", Args: args}); err != nil {
		return fmt.Errorf("setup container %q: %w", name, err)
	}
	return nil
}

// StartManagedContainer runs the global "weka local start", matching Python's
// ensure_managed_local_container(), which always (re)starts every configured container rather
// than one by name.
func StartManagedContainer(ctx context.Context, runner process.CommandRunner, name string) error {
	if _, err := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "start"}}); err != nil {
		return fmt.Errorf("start container %q: %w", name, err)
	}
	return nil
}

// EnsureContainerExec polls until the named container accepts exec commands, for up to 300s.
// Mirrors Python ensure_container_exec() at weka_runtime.py:3055.
func EnsureContainerExec(ctx context.Context, runner process.CommandRunner, c clock.Clock, name string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.EnsureContainerExec", "container", name)
	defer logger.End()
	logger.Info("ensuring container exec")

	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()

	err := clock.Poll(ctx, c, 2*time.Second, func() (bool, error) {
		_, runErr := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "exec", "--container", name, "--", "ls"}})
		if runErr != nil {
			logger.Info("waiting for container exec to become ready", "container", name)
			return false, nil
		}
		logger.Info("container exec ensured")
		return true, nil
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("container %q not exec-ready after 5 minutes: %w", name, err)
	}
	return err
}
