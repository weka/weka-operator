package kubernetes

import (
	"strconv"

	v1 "k8s.io/api/core/v1"
)

func NodeSatisfiesAffinity(node *v1.Node, affinity *v1.Affinity) bool {
	if affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		nodeSelectorTerms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		for _, term := range nodeSelectorTerms {
			if matchesNodeSelectorTerm(node, term) {
				return true
			}
		}
		// none of the terms matched
		return false
	}

	return true
}

// Helper func to check if a node matches a single NodeSelectorTerm
func matchesNodeSelectorTerm(node *v1.Node, term v1.NodeSelectorTerm) bool {
	for _, req := range term.MatchExpressions {
		switch req.Operator {
		case v1.NodeSelectorOpIn:
			if !contains(node.Labels[req.Key], req.Values) {
				return false
			}
		case v1.NodeSelectorOpNotIn:
			if contains(node.Labels[req.Key], req.Values) {
				return false
			}
		case v1.NodeSelectorOpExists:
			if _, exists := node.Labels[req.Key]; !exists {
				return false
			}
		case v1.NodeSelectorOpDoesNotExist:
			if _, exists := node.Labels[req.Key]; exists {
				return false
			}
		case v1.NodeSelectorOpGt, v1.NodeSelectorOpLt:
			valNumeric, err := strconv.Atoi(node.Labels[req.Key])
			if err != nil {
				return false
			}
			reqNumeric, err := strconv.Atoi(req.Values[0])
			if err != nil {
				return false
			}

			if req.Operator == v1.NodeSelectorOpGt && valNumeric < reqNumeric {
				return false
			}
			if req.Operator == v1.NodeSelectorOpLt && valNumeric > reqNumeric {
				return false
			}
		}
	}
	return true
}

func contains(value string, list []string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
