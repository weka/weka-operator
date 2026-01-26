package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"

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
		nodeNameStr, err := r.getCurrentPodNodeName()
		if err != nil {
			return nil, errors.Wrap(err, "failed to get current pod node name")
		}
		nodeName = weka.NodeName(nodeNameStr)
	}

	nodeAgentNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	pods, err := r.KubeService.GetPodsSimple(ctx, nodeAgentNamespace, string(nodeName), map[string]string{
		"control-plane": "weka-node-agent",
		"app":           "weka-node-agent",
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list node agent pods")
	}

	if len(pods) == 0 {
		return nil, errors.Errorf("no node agent pod found on node %s", nodeName)
	}

	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase == v1.PodRunning {
			err := r.validateNodeAgentImage(pod)
			if err != nil {
				return nil, err
			}
			return pod, nil
		}
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

func getNodeAgentContainerImage(pod *v1.Pod) (string, error) {
	for _, container := range pod.Spec.Containers {
		if container.Name == "node-agent" {
			return container.Image, nil
		}
	}
	return "", fmt.Errorf("node-agent container not found in pod %s", pod.Name)
}

func (r *containerReconcilerLoop) validateNodeAgentImage(nodeAgentPod *v1.Pod) error {
	nodeAgentImage, err := getNodeAgentContainerImage(nodeAgentPod)
	if err != nil {
		return errors.Wrap(err, "failed to get node-agent container image")
	}

	operatorImage := config.Config.OperatorImage
	if operatorImage != nodeAgentImage {
		return &NodeAgentImageMismatch{
			NodeAgentImage: nodeAgentImage,
			OperatorImage:  operatorImage,
		}
	}

	return nil
}
