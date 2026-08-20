package wekacluster

import (
	"time"

	"github.com/weka/weka-operator/internal/capacityplanner"
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

	reasonAutoFullDrivesPlanned           = "AutoFullDrivesPlanned"
	reasonAutoFullDrivesInfeasible        = "AutoFullDrivesInfeasible"
	reasonAutoFullDrivesNoSignedDrives    = "AutoFullDrivesNoSignedDrives"
	reasonAutoFullDrivesGrowthDetected    = "AutoFullDrivesGrowthDetected"
	reasonAutoFullDrivesGrowthDeferred    = "AutoFullDrivesGrowthDeferred"
	reasonAutoFullDrivesDrivesStranded    = "AutoFullDrivesDrivesStranded"
	reasonAutoFullDrivesPlacementDeferred = "AutoFullDrivesPlacementDeferred"
	reasonAutoFullDrivesNodeIneligible    = "AutoFullDrivesNodeIneligible"
	reasonAutoFullDrivesComputeLayout     = "AutoFullDrivesComputeLayout"
	reasonAutoFullDrivesWarning           = "AutoFullDrivesWarning"

	// reasonCapacityGrowthApplied lands on the WekaContainer, not the cluster, and is unthrottled — hence no
	// plannerEventSpecs row. It is named here only so the growth appliers and their tests share one spelling.
	reasonCapacityGrowthApplied = "CapacityGrowthApplied"
)

// plannerConvergedEventInterval throttles advisories describing a converged cluster — one that is
// permanently compute-limited but healthy. At one minute those re-fired forever and tripped Warning
// alerting. Reasons describing a transition or a hard stop keep the shorter window.
const plannerConvergedEventInterval = 15 * time.Minute

// plannerAggregateEventInterval throttles the fleet-wide aggregates, whose message names the affected node
// set. RecordEventThrottled keys on eventtype+reason and ignores the message, so a window also withholds an
// aggregate naming a *different* set: a node cordoned two minutes after the first event would otherwise wait
// out the whole converged-state window. Short enough that a changed set is reported promptly, long enough
// that a stable one is not re-posted every reconcile.
const plannerAggregateEventInterval = 3 * time.Minute

type plannerEventSpec struct {
	eventType string
	interval  time.Duration
}

var plannerEventSpecs = map[string]plannerEventSpec{
	reasonClusterCapacityPlanned:             {corev1.EventTypeNormal, time.Minute},
	reasonClusterCapacityInfeasible:          {corev1.EventTypeWarning, time.Minute},
	reasonClusterCapacityDeferred:            {corev1.EventTypeNormal, time.Minute},
	reasonClusterCapacityShrink:              {corev1.EventTypeNormal, time.Minute},
	reasonClusterCapacityOverProvisioned:     {corev1.EventTypeNormal, time.Minute},
	reasonClusterCapacityHeterogeneousGrowth: {corev1.EventTypeWarning, time.Minute},

	reasonAutoFullDrivesPlanned:        {corev1.EventTypeNormal, time.Minute},
	reasonAutoFullDrivesInfeasible:     {corev1.EventTypeWarning, time.Minute},
	reasonAutoFullDrivesNoSignedDrives: {corev1.EventTypeNormal, time.Minute},
	reasonAutoFullDrivesGrowthDetected: {corev1.EventTypeNormal, time.Minute},
	reasonAutoFullDrivesGrowthDeferred: {corev1.EventTypeWarning, plannerConvergedEventInterval},
	// Stranding is expected under a numDrives pin and a transient deferral clears itself, so neither is a
	// Warning — emitting them as such made a healthy converged cluster accumulate Warnings.
	reasonAutoFullDrivesDrivesStranded:    {corev1.EventTypeNormal, plannerAggregateEventInterval},
	reasonAutoFullDrivesPlacementDeferred: {corev1.EventTypeNormal, plannerAggregateEventInterval},
	// Normal, not Warning: withholding a node costs nothing on its own — the plan proceeds on the rest, and
	// when the loss does matter the plan turns infeasible and AutoFullDrivesInfeasible carries that as a
	// Warning. Aggregate window, not the converged one: cordon/taint is an administrative state that persists
	// for minutes-to-hours, but the set of cordoned nodes changes within that, and the throttle key cannot
	// tell one node list from another.
	reasonAutoFullDrivesNodeIneligible: {corev1.EventTypeNormal, plannerAggregateEventInterval},
	reasonAutoFullDrivesComputeLayout:  {corev1.EventTypeWarning, plannerConvergedEventInterval},
	reasonAutoFullDrivesWarning:        {corev1.EventTypeWarning, plannerConvergedEventInterval},
}

// emitPlannerEvent records message on the WekaCluster under reason, with that reason's policy.
func (r *wekaClusterReconcilerLoop) emitPlannerEvent(reason, message string) {
	spec, known := plannerEventSpecs[reason]
	if !known {
		// A reason with no row is a programming error that TestPlannerEventSpecsCoverEveryReason catches; still
		// emit rather than silently drop it.
		spec = plannerEventSpec{eventType: corev1.EventTypeWarning, interval: time.Minute}
	}
	_ = r.RecordEventThrottled(spec.eventType, reason, message, spec.interval) //nolint:errcheck // best effort
}

// autoFullDrivesWarningReasons gives each planner warning cause its own reason, so operators can filter with
// `--field-selector reason=` and alert on the actionable ones only.
var autoFullDrivesWarningReasons = map[capacityplanner.WarningKind]string{
	capacityplanner.WarningKindDrivesStranded: reasonAutoFullDrivesDrivesStranded,
	capacityplanner.WarningKindTransient:      reasonAutoFullDrivesPlacementDeferred,
	capacityplanner.WarningKindComputeLayout:  reasonAutoFullDrivesComputeLayout,
	capacityplanner.WarningKindNodeIneligible: reasonAutoFullDrivesNodeIneligible,
}

// autoFullDrivesWarningReason falls back to the catch-all so a kind added to the planner without a row here
// stays visible instead of being dropped.
func autoFullDrivesWarningReason(kind capacityplanner.WarningKind) string {
	if reason, ok := autoFullDrivesWarningReasons[kind]; ok {
		return reason
	}
	return reasonAutoFullDrivesWarning
}
