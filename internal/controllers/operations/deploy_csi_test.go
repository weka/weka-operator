package operations

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestIsCsiAccessibleLabelKey(t *testing.T) {
	for key, want := range map[string]bool{
		"topology.csi.weka.io/accessible":         true,
		"topology.tol.default.weka.io/accessible": true,
		"topology.csi.weka.io/global":             false,
		"topology.wekafs.csi/node":                false,
		"topology.tol.default.weka.io/node":       false,
		"foreign.weka.io/accessible":              false,
		"topology.kubernetes.io/accessible":       false,
	} {
		if got := IsCsiAccessibleLabelKey(key); got != want {
			t.Errorf("IsCsiAccessibleLabelKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestCsiTopologyLabelsServiceAccessible(t *testing.T) {
	for _, accessible := range []bool{true, false} {
		s := NewCsiTopologyLabelsService("csi.weka.io", "node-1", accessible)
		labels := s.GetExpectedCsiTopologyLabels()
		if _, ok := labels["topology.csi.weka.io/accessible"]; ok != accessible {
			t.Errorf("accessible=%v: labels %v", accessible, labels)
		}
		node := &corev1.Node{}
		node.Labels = map[string]string{"topology.csi.weka.io/accessible": "true", "topology.csi.weka.io/node": "node-1", "topology.csi.weka.io/transport": "wekafs", "other": "x"}
		if got := s.NodeHasExpectedCsiTopologyLabels(node); got != accessible {
			t.Errorf("accessible=%v: NodeHasExpectedCsiTopologyLabels = %v", accessible, got)
		}
	}
}
