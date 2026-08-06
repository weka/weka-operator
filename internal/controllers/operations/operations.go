package operations

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

type Operation interface {
	AsStep() lifecycle.Step
	GetSteps() []lifecycle.Step
	GetJsonResult() string
}

func AsRunFunc(op Operation) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		steps := op.GetSteps()
		stepsEngine := lifecycle.StepsEngine{
			Steps: steps,
		}
		return stepsEngine.Run(ctx)
	}
}

// previousOwnerResult returns the raw JSON result the owner recorded for its previous run, or ""
// when the owner kind carries no such field. WekaManualOperation and WekaPolicy spell it
// differently (Status.Result vs Status.LastResult), which is the only reason this switch exists.
func previousOwnerResult(ownerRef client.Object) string {
	switch owner := ownerRef.(type) {
	case *weka.WekaManualOperation:
		return owner.Status.Result
	case *weka.WekaPolicy:
		return owner.Status.LastResult
	default:
		return ""
	}
}

// ownerStatusIs reports whether the owner CR's status is already status, so callers can skip
// re-entering a finished state machine or avoid rewriting an unchanged terminal status.
func ownerStatusIs(ownerRef client.Object, status string) bool {
	switch owner := ownerRef.(type) {
	case *weka.WekaManualOperation:
		return owner.Status.Status == status
	case *weka.WekaPolicy:
		return owner.Status.Status == status
	default:
		return false
	}
}

// ownerDone reports whether the owner CR is already in the Done state.
func ownerDone(ownerRef client.Object) bool { return ownerStatusIs(ownerRef, "Done") }

// ownerFailed reports whether the owner CR is already in the Failed state.
func ownerFailed(ownerRef client.Object) bool { return ownerStatusIs(ownerRef, "Failed") }

// decodePreviousOwnerResult decodes the owner's previously recorded JSON result into T.
// Best-effort by design: returns nil on absence or any parse failure, since callers use it to
// carry counters across reconciles and must not fail an operation over an unreadable one.
func decodePreviousOwnerResult[T any](ownerRef client.Object) *T {
	raw := previousOwnerResult(ownerRef)
	if raw == "" {
		return nil
	}
	var prev T
	if err := json.Unmarshal([]byte(raw), &prev); err != nil {
		return nil
	}
	return &prev
}

// targetProxy pairs an ssdproxy container with its resolved node name. Shared by every operation
// that works node-by-node over the ssdproxy fleet (clean-stale-virtual-drives, rotate-ssdproxy).
type targetProxy struct {
	container weka.WekaContainer
	node      weka.NodeName
}

// resolveTargetProxies lists ssdproxy containers in the operator namespace, optionally filtered to
// nodes matching nodeSelector. ssdproxy containers are shared across clusters on a node and live in
// the operator namespace. nodeSelector is matched against the node's OWN labels (via GetNodes), not
// the container's. Proxies with no resolvable node affinity are skipped.
func resolveTargetProxies(ctx context.Context, kubeService kubernetes.KubeService, nodeSelector map[string]string) ([]targetProxy, error) {
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	proxies, err := kubeService.GetWekaContainersSimple(ctx, operatorNamespace, "", map[string]string{
		domain.WekaLabelMode: weka.WekaContainerModeSSDProxy,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ssdproxy containers")
	}

	var nodeFilter map[string]bool
	if len(nodeSelector) > 0 {
		nodes, err := kubeService.GetNodes(ctx, nodeSelector)
		if err != nil {
			return nil, errors.Wrap(err, "failed to list nodes for NodeSelector")
		}
		nodeFilter = make(map[string]bool, len(nodes))
		for i := range nodes {
			nodeFilter[nodes[i].Name] = true
		}
	}

	targets := make([]targetProxy, 0, len(proxies))
	for i := range proxies {
		node := proxies[i].GetNodeAffinity()
		if node == "" {
			continue
		}
		if nodeFilter != nil && !nodeFilter[string(node)] {
			continue
		}
		targets = append(targets, targetProxy{container: proxies[i], node: node})
	}
	return targets, nil
}

func ExecuteOperation(ctx context.Context, op Operation) error {
	step := op.AsStep()
	stepsEngine := lifecycle.StepsEngine{
		Steps: []lifecycle.Step{step},
	}
	return stepsEngine.Run(ctx)
}
