package util

import (
	v1 "k8s.io/api/core/v1"
)

func NodeSelectorMatchesNode(nodeSelector map[string]string, node *v1.Node) bool {
	for key, value := range nodeSelector {
		if labelVal, ok := node.Labels[key]; !ok || labelVal != value {
			return false
		}
	}
	return true
}
