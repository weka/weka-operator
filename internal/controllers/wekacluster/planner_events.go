package wekacluster

import (
	"time"

	"github.com/weka/weka-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
)

// planner_events.go is the whole cluster-level event surface of the two capacity-planner modes: one row per
// reason giving its severity, throttle window and throttle key. Emission sites name the reason, so reading
// one takes a single hop. Rows mirror the Events tables in
// doc/operator/deployment/{act-as-daemonset,cluster-capacity}.md, and a table test keeps the two in step.
// Per-container events (CapacityGrowthApplied, Unschedulable*Container) are absent: they are unthrottled
// Recorder.Event calls on the WekaContainer, so they carry no policy to centralise.

const (
	reasonClusterCapacityPlanned             = "ClusterCapacityPlanned"
	reasonClusterCapacityInfeasible          = "ClusterCapacityInfeasible"
	reasonClusterCapacityDeferred            = "ClusterCapacityDeferred"
	reasonClusterCapacityShrink              = "ClusterCapacityShrink"
	reasonClusterCapacityOverProvisioned     = "ClusterCapacityOverProvisioned"
	reasonClusterCapacityHeterogeneousGrowth = "ClusterCapacityHeterogeneousGrowth"

	reasonAutoFullDrivesContainersCreated  = "AutoFullDrivesContainersCreated"
	reasonAutoFullDrivesGrowth             = "AutoFullDrivesGrowth"
	reasonAutoFullDrivesUnusedDrivesOnNode = "AutoFullDrivesUnusedDrivesOnNode"
	reasonAutoFullDrivesNodeIneligible     = "AutoFullDrivesNodeIneligible"
	reasonAutoFullDrivesUnsignedDriveNodes = "AutoFullDrivesUnsignedDriveNodes"
	reasonAutoFullDrivesComputeCoresCapped = "AutoFullDrivesComputeCoresCapped"
	// reasonAutoFullDrivesHugepagesPinBelowAuto is also posted, throttled, on each affected WekaContainer.
	reasonAutoFullDrivesHugepagesPinBelowAuto = "AutoFullDrivesHugepagesPinBelowAuto"

	// reasonCapacityGrowthApplied lands on the WekaContainer, not the cluster, and is unthrottled — hence no
	// plannerEventSpecs row. It is named here only so the growth appliers and their tests share one spelling.
	reasonCapacityGrowthApplied = "CapacityGrowthApplied"
)

// plannerAggregateEventInterval throttles the fleet-wide aggregates, whose message names the affected node
// set. emitPlannerEvent keys on eventtype+reason+cause, so a distinct cause (e.g. a node going NotReady vs.
// one merely cordoned) gets its own window instead of waiting out one already open for a different cause. A
// changed node set under the *same* cause is still bounded by this window — short enough that it is reported
// promptly, long enough that a stable set is not re-posted every reconcile.
const plannerAggregateEventInterval = 3 * time.Minute

type plannerEventSpec struct {
	eventType string
	action    string
	interval  time.Duration
}

var plannerEventSpecs = map[string]plannerEventSpec{
	reasonClusterCapacityPlanned:             {corev1.EventTypeNormal, consts.ActionPlanCapacity, time.Minute},
	reasonClusterCapacityInfeasible:          {corev1.EventTypeWarning, consts.ActionPlanCapacity, time.Minute},
	reasonClusterCapacityDeferred:            {corev1.EventTypeNormal, consts.ActionPlanCapacity, time.Minute},
	reasonClusterCapacityShrink:              {corev1.EventTypeNormal, consts.ActionPlanCapacity, time.Minute},
	reasonClusterCapacityOverProvisioned:     {corev1.EventTypeNormal, consts.ActionPlanCapacity, time.Minute},
	reasonClusterCapacityHeterogeneousGrowth: {corev1.EventTypeWarning, consts.ActionPlanCapacity, time.Minute},

	reasonAutoFullDrivesContainersCreated:  {corev1.EventTypeNormal, consts.ActionPlanCapacity, time.Minute},
	reasonAutoFullDrivesGrowth:             {corev1.EventTypeNormal, consts.ActionPlanCapacity, time.Minute},
	reasonAutoFullDrivesUnusedDrivesOnNode: {corev1.EventTypeNormal, consts.ActionPlanCapacity, plannerAggregateEventInterval},
	reasonAutoFullDrivesNodeIneligible:     {corev1.EventTypeNormal, consts.ActionPlanCapacity, plannerAggregateEventInterval},
	reasonAutoFullDrivesUnsignedDriveNodes: {corev1.EventTypeWarning, consts.ActionPlanCapacity, time.Minute},
	reasonAutoFullDrivesComputeCoresCapped: {corev1.EventTypeWarning, consts.ActionPlanCapacity, 2 * time.Minute},

	reasonAutoFullDrivesHugepagesPinBelowAuto: {corev1.EventTypeWarning, consts.ActionPlanCapacity, 5 * time.Minute},
}

// emitPlannerEvent records message on the WekaCluster under reason, with that reason's policy. For
// reasons whose Warnings need their own throttle key per cause, use emitPlannerEventWithCause instead.
func (r *wekaClusterReconcilerLoop) emitPlannerEvent(reason, message string) {
	r.emitPlannerEventWithCause(reason, "", message)
}

// emitPlannerEventWithCause is emitPlannerEvent plus a cause that further keys the throttle, so a distinct
// condition under the same reason (e.g. a node going NotReady vs. one merely cordoned) gets its own window.
func (r *wekaClusterReconcilerLoop) emitPlannerEventWithCause(reason, cause, message string) {
	spec, known := plannerEventSpecs[reason]
	if !known {
		// A reason with no row is a programming error that TestPlannerEventSpecsCoverEveryReason catches; still
		// emit rather than silently drop it.
		spec = plannerEventSpec{eventType: corev1.EventTypeWarning, action: consts.ActionPlanCapacity, interval: time.Minute}
	}
	_ = r.RecordEventThrottledKeyed(spec.eventType, reason, cause, spec.action, message, spec.interval) //nolint:errcheck // best effort
}
