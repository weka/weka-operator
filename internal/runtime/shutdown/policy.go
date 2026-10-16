// Package shutdown orchestrates weka-container teardown: the operator-approval gate, the
// stop-loop/force-escalation pair, and drive-release waiting.
package shutdown

import "github.com/weka/weka-operator/internal/pkg/domain"

// Instructions is the operator-written shutdown contract.
type Instructions = domain.ShutdownInstructions

// StopPolicy returns whether mode waits for operator approval before stopping, and whether
// its first stop attempt is forced. This is the only place the per-mode approval rules live.
// Modes absent from the table (discovery, drivers-loader, drivers-builder, adhoc-op) never
// issue a `weka local stop` at all; approval/force are meaningless for them and both are
// returned false.
func StopPolicy(mode string) (approval, force bool) {
	switch mode {
	case "compute", "drive", "s3", "nfs", "smbw":
		return true, false
	case "client":
		return true, true
	case "data-services", "envoy", "telemetry", "ssdproxy", "drivers-dist", "adhoc-op-with-container":
		return false, true
	default:
		return false, false
	}
}
