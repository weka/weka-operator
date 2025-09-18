package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) getCurrentPodNodeName() (string, error) {
	if r.pod == nil {
		return "", errors.New("Pod is not set")
	}

	pod := r.pod

	if pod.Status.Phase != v1.PodRunning {
		err := fmt.Errorf("Pod is not running yet, current phase: %s", pod.Status.Phase)
		return "", err
	}

	if pod.Spec.NodeName != "" {
		return pod.Spec.NodeName, nil
	}

	return "", errors.New("Pod node name is not set")
}

func (r *containerReconcilerLoop) GetNodeAgentPod(ctx context.Context, nodeName weka.NodeName) (*v1.Pod, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "GetNodeAgentPod", "node", nodeName)
	defer end()

	if nodeName == "" {
		return nil, errors.New("node name is empty")
	}

	nodeAgentName := GetNodeAgentName(nodeName)
	nodeAgentNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	nodeAgentPod := &v1.Pod{}

	err = r.Get(ctx, client.ObjectKey{
		Name:      nodeAgentName,
		Namespace: nodeAgentNamespace,
	}, nodeAgentPod)

	if err != nil {
		return nil, errors.Wrap(err, "failed to get node agent pod")
	}

	if nodeAgentPod.Status.Phase == v1.PodRunning {
		return nodeAgentPod, nil
	}

	return nil, &NodeAgentPodNotRunning{}
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
