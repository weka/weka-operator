package operations

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Campaign state for migrate-to-drive-sharing, JSON-marshaled into status.result at runtime. It
// lives here rather than in pkg/weka-k8s-api since no CRD schema field references it.

// Per-container phases. As in rotate-ssdproxy there is no Failed phase: a container that cannot
// complete parks indefinitely rather than failing the campaign.
const (
	MigrateDriveSharingPhasePending  = "Pending"
	MigrateDriveSharingPhaseInFlight = "InFlight"
	MigrateDriveSharingPhaseDone     = "Done"
	MigrateDriveSharingPhaseSkipped  = "Skipped"
)

// Sub-phases of an InFlight container, in the order they are traversed. Persisted so the state is
// re-derivable after an operator restart.
const (
	MigrateDriveSharingSubPhaseGate      = "Gate"
	MigrateDriveSharingSubPhaseDraining  = "Draining"
	MigrateDriveSharingSubPhaseSigning   = "Signing"
	MigrateDriveSharingSubPhaseProxy     = "Proxy"
	MigrateDriveSharingSubPhaseRejoining = "Rejoining"
)

// Where a container's recorded drive capacity came from, since the two sources can disagree.
const (
	MigrateDriveSharingCapacityFromWeka       = "weka"
	MigrateDriveSharingCapacityFromAnnotation = "annotation"
)

// MigrateToDriveSharingResult is the campaign state, written to status.result as JSON each cycle.
type MigrateToDriveSharingResult struct {
	Cluster    string                                `json:"cluster"`
	Total      int                                   `json:"total"`
	Done       int                                   `json:"done"`
	Current    string                                `json:"current,omitempty"`
	Containers []MigrateToDriveSharingContainerState `json:"containers"`

	OldTotalBytes    int64 `json:"oldTotalBytes,omitempty"`
	NewTotalBytes    int64 `json:"newTotalBytes,omitempty"`
	ProvisionedBytes int64 `json:"provisionedBytes,omitempty"`
	// CapacityChecked latches the one-off capacity sanity check so it is not re-run (and its
	// shrink warning not re-emitted) once it has passed.
	CapacityChecked bool `json:"capacityChecked,omitempty"`

	Blocked []ClusterVerdict `json:"blocked,omitempty"`
	Err     string           `json:"err,omitempty"`
	// BlockedSince is when Plan first parked at campaign scope; stamped once, cleared on next success.
	BlockedSince *metav1.Time `json:"blockedSince,omitempty"`
}

// MigrateToDriveSharingContainerState tracks one exclusive drive container's migration. Serials,
// DriveUuids and CapacityBytes are recorded before the first mutation, because the container object
// they come from is deleted moments later and is the only place they exist.
type MigrateToDriveSharingContainerState struct {
	Node      weka.NodeName `json:"node"`
	Container string        `json:"container"`
	Phase     string        `json:"phase"`
	SubPhase  string        `json:"subPhase,omitempty"`

	Serials        []string `json:"serials,omitempty"`
	DriveUuids     []string `json:"driveUuids,omitempty"`
	CapacityBytes  int64    `json:"capacityBytes,omitempty"`
	CapacitySource string   `json:"capacitySource,omitempty"`

	Replacement  string `json:"replacement,omitempty"`
	SignOp       string `json:"signOp,omitempty"`
	SignAttempts int    `json:"signAttempts,omitempty"`
	// SignDone latches once the child sign-drives operation reported Done while serials were still
	// missing from the shared-drives annotation; it blocks any recreate of that child.
	SignDone bool `json:"signDone,omitempty"`
	// SignFailedAt gives the failed child sign operation a window in which it stays inspectable
	// before it is deleted and retried.
	SignFailedAt *metav1.Time `json:"signFailedAt,omitempty"`
	// ProxyRestarted records that this campaign already deleted the node's ssdproxy pod, so the
	// Proxy sub-phase verifies recovery instead of restarting it again.
	ProxyRestarted bool `json:"proxyRestarted,omitempty"`

	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// BlockedSince is when this container first parked while still Pending.
	BlockedSince *metav1.Time `json:"blockedSince,omitempty"`
	// Reason is refreshed every cycle so a stale reason never masks a changed situation.
	Reason string `json:"reason,omitempty"`
}
