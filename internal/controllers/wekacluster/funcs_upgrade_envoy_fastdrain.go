package wekacluster

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
)

// Copy of the Weka build's upgrade/container_hooks/envoy_hotrestart.sh; keep it in sync with the build.
//
//go:embed envoy_hotrestart.sh
var envoyHotRestartScript string

const (
	envoyScriptAdmin = "ADMIN=127.0.0.1:9901"
	// The hot restart's parent envoy exits after 30s; the margin covers s6 scheduling.
	envoyParentExitWait   = 35 * time.Second
	envoyMaxFastDrain     = 5 * time.Second
	envoyFastDrainTimeout = 90 * time.Second
	envoyHotRestartRetry  = 15 * time.Second
	fastDrainImmediate    = "immediate"
)

func (r *wekaClusterReconcilerLoop) envoyAdminURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", r.cluster.Status.Ports.LbAdminPort)
}

// awaitFastDrain hot-restarts envoy with the immediate drain policy, so the drain can be graceful, and waits until
// the policy is in effect and the old process is gone. The script is idempotent and re-run while envoy still
// reports another policy. Past envoyFastDrainTimeout into the retirement the drain goes on, non-graceful.
func (r *wekaClusterReconcilerLoop) awaitFastDrain(ctx context.Context, envoy *weka.WekaContainer, state envoyState, age time.Duration) error {
	if state.fastDrainReady() {
		r.reportOnce(envoy, v1.EventTypeNormal, "EnvoyFastDrainReady", fmt.Sprintf("envoy %s runs the immediate drain policy", envoy.Name))
		return nil
	}
	if age >= envoyFastDrainTimeout {
		r.reportOnce(envoy, v1.EventTypeWarning, "EnvoyFastDrainTimedOut",
			fmt.Sprintf("envoy %s did not reach the immediate drain policy, falling back to the non-graceful drain", envoy.Name))
		return nil
	}
	// Re-run at most every envoyHotRestartRetry: each run is a nested exec, and a hot restart takes seconds to show.
	if !state.immediate && r.Throttler.ShouldRun("envoy-hotrestart/"+string(envoy.UID), &throttling.ThrottlingSettings{
		Interval:                    envoyHotRestartRetry,
		DisableRandomPreSetInterval: true,
	}) {
		if err := r.hotRestartEnvoy(ctx, envoy); err != nil {
			return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(err, "hot-restarting envoy %s", envoy.Name), 3*time.Second)
		}
	}
	return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for envoy %s to run the immediate drain policy", envoy.Name), 3*time.Second)
}

func (r *wekaClusterReconcilerLoop) hotRestartEnvoy(ctx context.Context, envoy *weka.WekaContainer) error {
	script := strings.Replace(envoyHotRestartScript, envoyScriptAdmin,
		fmt.Sprintf("ADMIN=127.0.0.1:%d", r.cluster.Status.Ports.LbAdminPort), 1)
	_, err := r.execIn(ctx, envoy, "EnvoyHotRestart", time.Minute,
		"weka", "local", "exec", "-C", "envoy", "--", "/bin/bash", "-c", script)
	return err
}
