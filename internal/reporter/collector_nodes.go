package reporter

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/pkg/util"
)

// nodeSummary is the node projection sent to Weka Home. It carries only
// operator-relevant fields (not the full Node) to stay small and privacy-scoped.
type nodeSummary struct {
	Name              string            `json:"name"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	ExtendedResources map[string]string `json:"extendedResources,omitempty"`
	NodeInfo          nodeInfoSummary   `json:"nodeInfo"`
}

// nodeInfoSummary carries the driver-distribution-relevant subset of
// corev1.NodeSystemInfo.
type nodeInfoSummary struct {
	KernelVersion           string `json:"kernelVersion,omitempty"`
	OSImage                 string `json:"osImage,omitempty"`
	OperatingSystem         string `json:"operatingSystem,omitempty"`
	Architecture            string `json:"architecture,omitempty"`
	ContainerRuntimeVersion string `json:"containerRuntimeVersion,omitempty"`
	KubeletVersion          string `json:"kubeletVersion,omitempty"`
}

// collectNodes lists all nodes and returns projections for those matching any
// of the provided selectors. An empty/nil selector matches all nodes.
func collectNodes(ctx context.Context, c client.Client, selectors []map[string]string) ([]nodeSummary, error) {
	allNodes := &corev1.NodeList{}
	if err := c.List(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("list Nodes: %w", err)
	}

	var result []nodeSummary
	for i := range allNodes.Items {
		node := &allNodes.Items[i]
		if matchesAny(node, selectors) {
			result = append(result, projectNode(node))
		}
	}
	return result, nil
}

// matchesAny reports whether node matches at least one of the selectors
// (label-subset, via the shared util.NodeSelectorMatchesNode). An empty/nil
// selector matches every node; no selectors ⇒ no match.
func matchesAny(node *corev1.Node, selectors []map[string]string) bool {
	for _, sel := range selectors {
		if util.NodeSelectorMatchesNode(sel, node) {
			return true
		}
	}
	return false
}

// projectNode builds a nodeSummary from a Node, keeping only weka.io-domain labels
// (weka.io/ and any *.weka.io/ subdomain, e.g. topology.*.weka.io/), weka.io/*
// annotations, weka.io/* extended resources from status.capacity, and the relevant
// nodeInfo fields.
func projectNode(node *corev1.Node) nodeSummary {
	s := nodeSummary{
		Name: node.Name,
		NodeInfo: nodeInfoSummary{
			KernelVersion:           node.Status.NodeInfo.KernelVersion,
			OSImage:                 node.Status.NodeInfo.OSImage,
			OperatingSystem:         node.Status.NodeInfo.OperatingSystem,
			Architecture:            node.Status.NodeInfo.Architecture,
			ContainerRuntimeVersion: node.Status.NodeInfo.ContainerRuntimeVersion,
			KubeletVersion:          node.Status.NodeInfo.KubeletVersion,
		},
	}

	for k, v := range node.Labels {
		if isWekaLabel(k) {
			if s.Labels == nil {
				s.Labels = make(map[string]string)
			}
			s.Labels[k] = v
		}
	}

	for k, v := range node.Annotations {
		if strings.HasPrefix(k, "weka.io/") {
			if s.Annotations == nil {
				s.Annotations = make(map[string]string)
			}
			s.Annotations[k] = v
		}
	}

	for k, q := range node.Status.Capacity {
		ks := string(k)
		if strings.HasPrefix(ks, "weka.io/") {
			if s.ExtendedResources == nil {
				s.ExtendedResources = make(map[string]string)
			}
			s.ExtendedResources[ks] = q.String()
		}
	}

	return s
}

// isWekaLabel reports whether a label key is in the weka.io domain: the weka.io/
// prefix or any *.weka.io/ subdomain (e.g. topology.*.weka.io/). A valid label key
// has exactly one '/', so the ".weka.io/" check is equivalent to a domain-suffix match.
func isWekaLabel(key string) bool {
	return strings.HasPrefix(key, "weka.io/") || strings.Contains(key, ".weka.io/")
}
