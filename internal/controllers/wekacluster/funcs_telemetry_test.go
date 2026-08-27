package wekacluster

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The telemetry hash gates EnsureTelemetry's whole body, and
// SkipDefaultFilesystemCreation gates enableAuditDefaultFs inside it. If the flag
// were invisible to the hash, clearing it would never re-enable audit on the
// default filesystem.
func TestCalculateTelemetryHash_ReactsToSkipDefaultFilesystemCreation(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	newLoop := func(skip *bool) *wekaClusterReconcilerLoop {
		cluster := &weka.WekaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		}
		cluster.Spec.Overrides = &weka.WekaClusterSpecOverrides{SkipDefaultFilesystemCreation: skip}
		cluster.Spec.Telemetry = &weka.TelemetryConfig{
			Exports: []weka.TelemetryExport{{Name: "splunk", Sources: []string{"audit"}}},
		}
		return &wekaClusterReconcilerLoop{cluster: cluster}
	}

	ctx := context.Background()
	unset := newLoop(nil).calculateTelemetryHash(ctx, newLoop(nil).cluster.Spec.Telemetry)
	skipFalse := newLoop(boolPtr(false)).calculateTelemetryHash(ctx, newLoop(boolPtr(false)).cluster.Spec.Telemetry)
	skipTrue := newLoop(boolPtr(true)).calculateTelemetryHash(ctx, newLoop(boolPtr(true)).cluster.Spec.Telemetry)

	if unset != skipFalse {
		t.Errorf("nil and explicit false must hash identically, got %s vs %s", unset, skipFalse)
	}
	if skipTrue == skipFalse {
		t.Fatalf("hash must change when the flag is set, both were %s", skipTrue)
	}
}
