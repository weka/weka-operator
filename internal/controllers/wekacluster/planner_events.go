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

type throttleKey int

const (
	// keyPerReason: one event per reason per window.
	keyPerReason throttleKey = iota
	// keyPerNode: one event per node per window, so N constrained nodes each get one instead of the first
	// starving the rest. Keying on the message instead would post a fresh event every time an embedded
	// number drifted.
	keyPerNode
)

type plannerEventSpec struct {
	eventType string
	interval  time.Duration
	key       throttleKey
}

var plannerEventSpecs = map[string]plannerEventSpec{
	reasonClusterCapacityPlanned:             {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonClusterCapacityInfeasible:          {corev1.EventTypeWarning, time.Minute, keyPerReason},
	reasonClusterCapacityDeferred:            {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonClusterCapacityShrink:              {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonClusterCapacityOverProvisioned:     {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonClusterCapacityHeterogeneousGrowth: {corev1.EventTypeWarning, time.Minute, keyPerReason},

	reasonAutoFullDrivesPlanned:        {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonAutoFullDrivesInfeasible:     {corev1.EventTypeWarning, time.Minute, keyPerReason},
	reasonAutoFullDrivesNoSignedDrives: {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonAutoFullDrivesGrowthDetected: {corev1.EventTypeNormal, time.Minute, keyPerReason},
	reasonAutoFullDrivesGrowthDeferred: {corev1.EventTypeWarning, plannerConvergedEventInterval, keyPerReason},
	// Stranding is expected under a numDrives pin and a transient deferral clears itself, so neither is a
	// Warning — emitting them as such made a healthy converged cluster accumulate Warnings.
	reasonAutoFullDrivesDrivesStranded:    {corev1.EventTypeNormal, plannerConvergedEventInterval, keyPerReason},
	reasonAutoFullDrivesPlacementDeferred: {corev1.EventTypeNormal, plannerConvergedEventInterval, keyPerNode},
	// Normal, not Warning: withholding a node costs nothing on its own — the plan proceeds on the rest, and
	// when the loss does matter the plan turns infeasible and AutoFullDrivesInfeasible carries that as a
	// Warning. Keyed per node so one node's condition cannot starve another's out of the throttle window.
	reasonAutoFullDrivesNodeIneligible: {corev1.EventTypeNormal, plannerConvergedEventInterval, keyPerNode},
	reasonAutoFullDrivesComputeLayout:  {corev1.EventTypeWarning, plannerConvergedEventInterval, keyPerReason},
	reasonAutoFullDrivesWarning:        {corev1.EventTypeWarning, plannerConvergedEventInterval, keyPerReason},
}

// emitPlannerEvent records message on the WekaCluster under reason, with that reason's policy. subject is the
// node name, used only by keyPerNode rows.
func (r *wekaClusterReconcilerLoop) emitPlannerEvent(reason, subject, message string) {
	spec, known := plannerEventSpecs[reason]
	if !known {
		// A reason with no row is a programming error that TestPlannerEventSpecsCoverEveryReason catches; still
		// emit rather than silently drop it.
		spec = plannerEventSpec{eventType: corev1.EventTypeWarning, interval: time.Minute}
	}
	if spec.key == keyPerNode {
		_ = r.RecordEventThrottledPerSubject(spec.eventType, reason, subject, message, spec.interval) //nolint:errcheck // best effort
		return
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
