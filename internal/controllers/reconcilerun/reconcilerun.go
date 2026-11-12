// Package reconcilerun wraps StepsEngine.RunAsReconcilerResponse with a requeue floor for channel errors.
package reconcilerun

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/pkg/util/channelerr"
)

const (
	channelFloor  = 30 * time.Second
	channelJitter = 30 * time.Second
)

// Run is RunAsReconcilerResponse, except that a failed API server / exec channel requeues no sooner than
// 30s plus up to 30s of jitter.
func Run(ctx context.Context, engine *lifecycle.StepsEngine) (ctrl.Result, error) {
	err := engine.Run(ctx)
	res := lifecycle.ResultFor(ctx, err)
	if channelerr.Is(err) {
		res.RequeueAfter = max(res.RequeueAfter, channelFloor+rand.N(channelJitter))
	}
	return res, nil
}
