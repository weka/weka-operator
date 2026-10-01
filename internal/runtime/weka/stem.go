package weka

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/weka-operator/internal/runtime/process"
)

// EnsureStemContainer creates a minimal "stem" Weka container if it doesn't already exist.
// Mirrors Python ensure_stem_container() at weka_runtime.py:3019.
func EnsureStemContainer(ctx context.Context, runner process.CommandRunner, name string, port int) error {
	script := fmt.Sprintf(`
if [ -d /driver-toolkit-shared ]; then
    mkdir -p /lib/modules
    mkdir -p /usr/src
    mount -o bind /driver-toolkit-shared/lib/modules /lib/modules
    mount -o bind /driver-toolkit-shared/usr/src /usr/src
fi
weka local ps | grep %s || weka local setup container --name %s --net udp --base-port %d --no-start --disable
`, name, name, port)
	_, err := runner.Run(ctx, process.Shell(script))
	return err
}

// localContainer is the subset of `weka local ps --json` container fields needed for lookups.
type localContainer struct {
	Name string `json:"name"`
}

// FindLocalContainer returns true if a container with the exact given name exists.
// Mirrors Python find_local_container() at weka_runtime.py.
func FindLocalContainer(ctx context.Context, runner process.CommandRunner, name string) (bool, error) {
	res, err := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "ps", "--json"}})
	if err != nil {
		return false, fmt.Errorf("weka local ps --json: %w", err)
	}
	return containsContainerName(res.Stdout, name)
}

// containsContainerName parses `weka local ps --json` output and reports whether it contains a
// container with the exact given name. Split out from FindLocalContainer for testing without a
// CommandRunner.
func containsContainerName(psJSON []byte, name string) (bool, error) {
	var containers []localContainer
	if err := json.Unmarshal(psJSON, &containers); err != nil {
		return false, fmt.Errorf("parse weka local ps --json: %w", err)
	}
	for _, c := range containers {
		if c.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// StartStemCLI starts "weka local start" as a background, non-restarting process: the command
// blocks forever once the container is up, so it cannot be run via CommandRunner.Run.
// Mirrors Python start_stem_container() at weka_runtime.py:3041.
func StartStemCLI(ctx context.Context, pm process.Launcher, name string) (*process.Handle, error) {
	return pm.StartProcess(ctx, name, process.Command{Path: "weka", Args: []string{"local", "start"}})
}
