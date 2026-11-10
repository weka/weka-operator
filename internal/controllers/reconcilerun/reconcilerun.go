// Package reconcilerun wraps StepsEngine.RunAsReconcilerResponse with a requeue floor for channel errors.
package reconcilerun

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/pkg/util/podexec"
)

const (
	channelFloor  = 30 * time.Second
	channelJitter = 30 * time.Second
)

// Cause peels StepRunError and WaitError, which have no Unwrap, down to the error a step returned.
func Cause(err error) error {
	for {
		switch e := err.(type) {
		case *lifecycle.StepRunError:
			err = e.Err
		case *lifecycle.WaitError:
			err = e.Err
		default:
			return err
		}
	}
}

// Run is RunAsReconcilerResponse, except that a failed API server / exec channel requeues no sooner than
// 30s plus up to 30s of jitter.
func Run(ctx context.Context, engine *lifecycle.StepsEngine) (ctrl.Result, error) {
	err := engine.Run(ctx)
	if err == nil {
		instrumentation.CurrentSpanLogger(ctx).Info("Reconciliation steps completed successfully")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	res := resultFor(ctx, err)
	if podexec.IsChannelError(Cause(err)) {
		res.RequeueAfter = max(res.RequeueAfter, channelFloor+rand.N(channelJitter))
	}
	return res, nil
}

func resultFor(ctx context.Context, err error) ctrl.Result {
	logger := instrumentation.CurrentSpanLogger(ctx)
	var runErr *lifecycle.StepRunError
	for e := err; ; {
		next, ok := e.(*lifecycle.StepRunError)
		if !ok {
			break
		}
		runErr = next
		e = next.Err
	}
	if runErr != nil {
		switch inner := runErr.Err.(type) {
		case *lifecycle.WaitError:
			logger.Info("waiting for conditions to be met", "error", err)
			if inner.Duration > 0 {
				return ctrl.Result{RequeueAfter: inner.Duration}
			}
			return ctrl.Result{RequeueAfter: 3 * time.Second}
		case *lifecycle.AbortedByPredicate:
			logger.Info("aborted by predicate", "error", err)
			return ctrl.Result{RequeueAfter: 3 * time.Second}
		}
	}
	logger.SetError(err, "Error processing reconciliation steps")
	return ctrl.Result{RequeueAfter: 10 * time.Second}
}
