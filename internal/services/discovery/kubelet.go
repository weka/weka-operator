package discovery

import (
	"context"
	"encoding/json"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DetectKubeletCPUManager queries the node's kubelet configz endpoint via the Kubernetes API proxy and
// returns its cpuManagerPolicy and whether it is static with cpuManagerPolicyOptions["full-pcpus-only"]="true".
// Requires nodes/proxy get permission in the operator's ClusterRole.
func DetectKubeletCPUManager(ctx context.Context, restConfig *rest.Config, nodeName string) (policy string, fullPcpusOnly bool, err error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", false, err
	}

	raw, err := clientset.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes", nodeName, "proxy", "configz").
		DoRaw(ctx)
	if err != nil {
		return "", false, err
	}

	var cz struct {
		KubeletConfig struct {
			CPUManagerPolicy        string            `json:"cpuManagerPolicy"`
			CPUManagerPolicyOptions map[string]string `json:"cpuManagerPolicyOptions"`
		} `json:"kubeletconfig"`
	}
	if err := json.Unmarshal(raw, &cz); err != nil {
		return "", false, err
	}

	kc := cz.KubeletConfig
	return kc.CPUManagerPolicy, kc.CPUManagerPolicy == "static" && kc.CPUManagerPolicyOptions["full-pcpus-only"] == "true", nil
}
