package wekacluster

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
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
	fastDrainImmediate    = "immediate"
	fastDrainFallback     = "fallback"
)

type envoyServerInfo struct {
	CommandLineOptions struct {
		DrainStrategy string `json:"drain_strategy"`
		DrainTime     string `json:"drain_time"`
	} `json:"command_line_options"`
}

// fastDrainReady reports whether envoy runs the immediate drain policy with a short drain time and the
// hot-restarted parent has had time to exit.
func fastDrainReady(serverInfo string, hotRestartAt, now time.Time) (bool, error) {
	var info envoyServerInfo
	if err := json.Unmarshal([]byte(serverInfo), &info); err != nil {
		return false, errors.Wrap(err, "parse envoy server_info")
	}
	drainTime, err := time.ParseDuration(info.CommandLineOptions.DrainTime)
	if err != nil {
		return false, errors.Wrapf(err, "parse envoy drain_time %q", info.CommandLineOptions.DrainTime)
	}
	return strings.EqualFold(info.CommandLineOptions.DrainStrategy, fastDrainImmediate) &&
		drainTime <= envoyMaxFastDrain &&
		now.Sub(hotRestartAt) >= envoyParentExitWait, nil
}

func drainListenersPath(graceful bool) string {
	if graceful {
		return "/drain_listeners?graceful"
	}
	return "/drain_listeners"
}

func (r *wekaClusterReconcilerLoop) envoyAdminURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", r.cluster.Status.Ports.LbAdminPort)
}

// awaitFastDrain hot-restarts envoy with the immediate drain policy and waits until it is in effect, so the
// drain can be graceful. Past envoyFastDrainTimeout it records the fallback to the non-graceful drain.
func (r *wekaClusterReconcilerLoop) awaitFastDrain(ctx context.Context, envoy *weka.WekaContainer) error {
	if _, ok := envoy.Annotations[consts.AnnotationEnvoyFastDrain]; ok {
		return nil
	}

	started, first, err := r.annotateStart(ctx, envoy, consts.AnnotationEnvoyHotRestartStarted)
	if err != nil {
		return err
	}
	if first {
		if err := r.hotRestartEnvoy(ctx, envoy); err != nil {
			return err
		}
	}

	ready, checkErr := r.envoyFastDrainReady(ctx, envoy, started)
	if ready {
		_ = r.RecordEvent(v1.EventTypeNormal, "EnvoyFastDrainReady", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
			fmt.Sprintf("envoy %s runs the immediate drain policy", envoy.Name))
		return r.setEnvoyAnnotation(ctx, envoy, consts.AnnotationEnvoyFastDrain, fastDrainImmediate)
	}
	if time.Since(started) >= envoyFastDrainTimeout {
		instrumentation.CurrentSpanLogger(ctx).Warn("envoy did not reach the immediate drain policy in time, using the non-graceful drain",
			"envoy", envoy.Name, "error", checkErr)
		_ = r.RecordEvent(v1.EventTypeWarning, "EnvoyFastDrainTimedOut", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
			fmt.Sprintf("envoy %s did not reach the immediate drain policy, falling back to the non-graceful drain", envoy.Name))
		return r.setEnvoyAnnotation(ctx, envoy, consts.AnnotationEnvoyFastDrain, fastDrainFallback)
	}
	if checkErr != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(checkErr, "checking envoy %s drain policy", envoy.Name), 3*time.Second)
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

func (r *wekaClusterReconcilerLoop) envoyFastDrainReady(ctx context.Context, envoy *weka.WekaContainer, hotRestartAt time.Time) (bool, error) {
	out, err := r.execIn(ctx, envoy, "EnvoyServerInfo", 10*time.Second, "curl", "-sSf", r.envoyAdminURL()+"/server_info")
	if err != nil {
		return false, err
	}
	return fastDrainReady(out, hotRestartAt, time.Now())
}

func (r *wekaClusterReconcilerLoop) setEnvoyAnnotation(ctx context.Context, envoy *weka.WekaContainer, key, value string) error {
	patch := client.MergeFrom(envoy.DeepCopy())
	if envoy.Annotations == nil {
		envoy.Annotations = map[string]string{}
	}
	envoy.Annotations[key] = value
	return errors.Wrapf(r.getClient().Patch(ctx, envoy, patch), "failed to annotate envoy %s with %s", envoy.Name, key)
}
