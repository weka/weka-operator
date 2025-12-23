package resources

import v1 "k8s.io/api/core/v1"

func NodeIsReady(node *v1.Node) bool {
	if node == nil {
		return false
	}
	// check if the node has a NodeReady condition set to True
	isNodeReady := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
			isNodeReady = true
			break
		}
	}
	return isNodeReady
}
