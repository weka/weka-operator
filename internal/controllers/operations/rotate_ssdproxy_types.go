package operations

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These types are the rotate-ssdproxy campaign's cross-reconcile state, JSON-marshaled into
// status.result at runtime. They live here, not in pkg/weka-k8s-api, since nothing in the CRD
// schema references them as a struct field.

// Node phases for RotateSsdProxyNodeState.Phase. There is deliberately no Failed phase: a node
// that cannot complete parks indefinitely rather than failing (see dev_doc/ssdproxy-rotation.md).
const (
	RotateSsdProxyPhasePending  = "Pending"
	RotateSsdProxyPhaseInFlight = "InFlight"
	RotateSsdProxyPhaseDone     = "Done"
	RotateSsdProxyPhaseSkipped  = "Skipped"
)

// RotateSsdProxyResult is the rotate-ssdproxy campaign state, written to status.result as JSON
// each cycle. It is the framework's cross-reconcile state mechanism for this operation.
type RotateSsdProxyResult struct {
	TargetImage string                    `json:"targetImage"`
	Total       int                       `json:"total"`
	Done        int                       `json:"done"`
	CurrentNode string                    `json:"currentNode,omitempty"`
	Nodes       []RotateSsdProxyNodeState `json:"nodes"`
	Blocked     []ClusterVerdict          `json:"blocked,omitempty"`
	Err         string                    `json:"err,omitempty"`
	// BlockedSince is when Plan first parked at campaign scope; stamped once, cleared on next success.
	BlockedSince *metav1.Time `json:"blockedSince,omitempty"`
}

// RotateSsdProxyNodeState tracks one node's progress through the rotation campaign.
type RotateSsdProxyNodeState struct {
	Node          weka.NodeName `json:"node"`
	ProxyName     string        `json:"proxyName"`
	Phase         string        `json:"phase"` // Pending | InFlight | Done | Skipped
	PreviousImage string        `json:"previousImage,omitempty"`
	Image         string        `json:"image,omitempty"`
	StartedAt     *metav1.Time  `json:"startedAt,omitempty"`
	// BlockedSince is when the gate first refused this node; cleared once unblocked or patched.
	BlockedSince *metav1.Time `json:"blockedSince,omitempty"`
	// Reason is refreshed every cycle so a stale reason never masks a changed situation.
	Reason string `json:"reason,omitempty"`
}
