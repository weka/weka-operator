package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

func (r *containerReconcilerLoop) waitPodReady(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)
	pod := r.pod
	container := r.container

	if pod.Status.Phase != v1.PodRunning {
		logger.Info("Pod is not running yet")
		return errors.New("Pod is not running yet")
	}

	if container.Status.Status != weka.Running {
		logger.Info("Container is not fully running yet", "status", container.Status.Status)
		return errors.New("Container is not fully running yet")
	}

	if !container.IsServiceContainer() && !container.IsSSDProxyContainer() {
		// Check STATUS == READY (skip if InternalStatus not yet populated)
		if container.Status.InternalStatus != "" && container.Status.InternalStatus != "READY" {
			logger.Info("Container is not READY yet", "status", container.Status.InternalStatus)
			return lifecycle.NewWaitError(fmt.Errorf("container status is not READY: %s", container.Status.InternalStatus))
		}

		// ReconcileWekaLocalStatus leaves this nil when it could not read `weka local ps --json` -
		// most notably when it bails out early on a NotReady node. Without it the lease and
		// IO-process gates below would all read zero values and silently pass, letting a rolling
		// upgrade advance past a container we know nothing about. Wait instead.
		if r.localContainer == nil {
			logger.Info("Weka local status is not available yet")
			return lifecycle.NewWaitError(errors.New("weka local status is not available yet"))
		}

		// Check VALID LEASE (only available in Weka >= 5.1.2; nil means field absent, skip)
		if r.localContainer.InternalStatus.HasLease != nil && !*r.localContainer.InternalStatus.HasLease {
			logger.Info("Container does not have a valid lease")
			return lifecycle.NewWaitError(errors.New("container does not have a valid lease"))
		}

		ioProcessesNotUp, hasIoProcessesNotUp := r.localContainer.InternalStatus.IoProcessesNotUp()
		if hasIoProcessesNotUp {
			// Not up yet: drop any previously recorded "IO processes up" anchor, and keep waiting
			// (uncapped - we don't give up and proceed anyway).
			if _, ok := container.Status.Timestamps[string(weka.TimestampIoProcessesUp)]; ok {
				delete(container.Status.Timestamps, string(weka.TimestampIoProcessesUp))
				if updateErr := r.Status().Update(ctx, container); updateErr != nil {
					return updateErr
				}
			}

			msg := fmt.Sprintf("container has IO processes not up: %s", ioProcessesNotUp)

			// The wait is uncapped by design, so also raise an event: otherwise a permanently
			// wedged IO process stalls the whole cluster's rolling upgrade with no signal outside
			// the operator log.
			_ = r.RecordEventThrottled(v1.EventTypeWarning, "IoProcessesNotUp", consts.ActionUpgrade, msg, time.Minute) //nolint:errcheck // error return value intentionally not checked

			// Logged at Info, not Debug: the wait below is uncapped, so the reported
			// process ids have to be visible at the default log level.
			logger.Info("Container has IO processes not up", "io_processes_not_up", ioProcessesNotUp)
			return lifecycle.NewWaitError(errors.New(msg))
		}

		// Checked before the settle wait below so the settle window does not start ticking while the
		// cluster still reports the old version.
		if r.container.ShouldJoinCluster() {
			if err := r.verifyClusterContainerApplied(ctx); err != nil {
				return err
			}
		}

		// IO processes are up. If a settle period is configured, hold off reporting the pod ready
		// until it has elapsed since IO processes were first observed up. Resolved here rather than
		// above so the not-up path does not depend on reading the owner object. The override lives on
		// whichever object owns the container - WekaClient for clients, WekaCluster for the rest.
		waitSince := config.Config.Timeouts.WaitSinceIoProcessesUpTimeout

		if r.container.ShouldJoinCluster() {
			override, overrideErr := r.resolveWaitSinceIoProcessesUpOverride(ctx)
			switch {
			case overrideErr == nil:
				if override != nil {
					waitSince = override.Duration
				}
			case errors.Is(overrideErr, errNoOverrideOwner):
				// Nothing to retry: proceed on the operator-wide default.
				logger.Error(overrideErr, "No owner override available, using default waitSinceIoProcessesUpTimeout")
			default:
				// Requeue instead of using the default: that default is 0 in the shipped chart, so a
				// transient read failure would collapse a configured settle window to no wait at all.
				return overrideErr
			}
		}

		if waitSince > 0 {
			anchor, ok := container.Status.Timestamps[string(weka.TimestampIoProcessesUp)]

			// An anchor older than the pod belongs to a previous one
			if ok && pod.Status.StartTime != nil && anchor.Time.Before(pod.Status.StartTime.Time) {
				ok = false
			}

			if !ok {
				container.Status.Timestamps[string(weka.TimestampIoProcessesUp)] = metav1.Time{Time: time.Now()}
				if updateErr := r.Status().Update(ctx, container); updateErr != nil {
					return updateErr
				}
				return lifecycle.NewWaitErrorWithDuration(errors.New("waiting for IO processes to settle"), waitSince)
			}

			if elapsed := time.Since(anchor.Time); elapsed < waitSince {
				return lifecycle.NewWaitErrorWithDuration(
					fmt.Errorf("waiting for IO processes to settle, %v elapsed of %v", elapsed, waitSince),
					max(waitSince-elapsed, time.Second),
				)
			}
		}
	}

	return nil
}
