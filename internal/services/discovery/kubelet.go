package discovery

import (
	"context"
	"encoding/json"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DetectKubeletFullPcpusOnly queries the node's kubelet configz endpoint via the
// Kubernetes API proxy and returns true if the kubelet is configured with
// cpuManagerPolicy=static and cpuManagerPolicyOptions["full-pcpus-only"]="true".
// Requires nodes/proxy get permission in the operator's ClusterRole.
func DetectKubeletFullPcpusOnly(ctx context.Context, restConfig *rest.Config, nodeName string) (bool, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return false, err
	}

	raw, err := clientset.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes", nodeName, "proxy", "configz").
		DoRaw(ctx)
	if err != nil {
		return false, err
	}

	var cz struct {
		KubeletConfig struct {
			CPUManagerPolicy        string            `json:"cpuManagerPolicy"`
			CPUManagerPolicyOptions map[string]string `json:"cpuManagerPolicyOptions"`
		} `json:"kubeletconfig"`
	}
	if err := json.Unmarshal(raw, &cz); err != nil {
		return false, err
	}

	return cz.KubeletConfig.CPUManagerPolicy == "static" &&
		cz.KubeletConfig.CPUManagerPolicyOptions["full-pcpus-only"] == "true", nil
}
