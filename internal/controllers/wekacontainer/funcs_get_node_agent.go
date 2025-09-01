package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) getNodeAgentPods(ctx context.Context) ([]v1.Pod, error) {
	if r.node == nil {
		return nil, errors.New("Node is not set")
	}

	if !NodeIsReady(r.node) {
		err := errors.New("Node is not ready")
		return nil, lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}
	//TODO: We can replace this call with GetWekaContainerSimple (and remove index for pods nodenames) if we move nodeAgent to be wekacontainer
	pods, err := r.KubeService.GetPodsSimple(ctx, ns, r.node.Name, map[string]string{
		"app.kubernetes.io/component": "weka-node-agent",
	})
	if err != nil {
		return nil, err
	}

	return pods, nil
}

func (r *containerReconcilerLoop) findAdjacentNodeAgent(ctx context.Context, pod *v1.Pod) (*v1.Pod, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "findAdjacentNodeAgent", "node", r.container.GetNodeAffinity())
	defer end()

	agentPods, err := r.getNodeAgentPods(ctx)
	var waitErr *lifecycle.WaitError
	if err != nil && errors.As(err, &waitErr) {
		return nil, err
	}
	if err != nil {
		err = fmt.Errorf("failed to get node agent pods: %w", err)
		return nil, err
	}
	if len(agentPods) == 0 {
		return nil, errors.New("There are no agent pods on node")
	}

	var targetNodeName string
	if r.container.GetNodeAffinity() != "" {
		targetNodeName = string(r.container.GetNodeAffinity())
	} else {
		if r.pod == nil {
			return nil, errors.New("Pod is nil and no affinity on container")
		}
		targetNodeName = pod.Spec.NodeName
	}

	for _, agentPod := range agentPods {
		if agentPod.Spec.NodeName == targetNodeName {
			if agentPod.Status.Phase == v1.PodRunning {
				return &agentPod, nil
			}
			return nil, &NodeAgentPodNotRunning{}
		}
	}

	return nil, errors.New("No agent pod found on the same node")
}

// a hack putting this global, but also not a harmful one
var nodeAgentToken string
var nodeAgentLastPull time.Time

func (r *containerReconcilerLoop) getNodeAgentToken(ctx context.Context) (string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getNodeAgentToken")
	defer end()

	if nodeAgentToken != "" && time.Since(nodeAgentLastPull) < time.Minute {
		return nodeAgentToken, nil
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return "", err
	}

	secret, err := r.KubeService.GetSecret(ctx, config.Config.Metrics.NodeAgentSecretName, ns)
	if err != nil {
		return "", err
	}
	if secret == nil {
		logger.Info("No secret found")
		return "", errors.New("No secret found")
	}

	tokenRaw := secret.Data["token"]
	token := string(tokenRaw)
	if token == "" {
		logger.Info("No token found")
		return "", errors.New("No token found")
	}
	nodeAgentToken = token
	nodeAgentLastPull = time.Now()

	return token, nil
}
