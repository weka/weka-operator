package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// withCsi sets the CSI operator-config knobs for the duration of the test,
// restoring originals via t.Cleanup.
func withCsi(t *testing.T, enabled, storageClassCreationDisabled bool) {
	t.Helper()
	prevEnabled := globalconfig.Config.Csi.Enabled
	prevDisabled := globalconfig.Config.Csi.StorageClassCreationDisabled
	globalconfig.Config.Csi.Enabled = enabled
	globalconfig.Config.Csi.StorageClassCreationDisabled = storageClassCreationDisabled
	t.Cleanup(func() {
		globalconfig.Config.Csi.Enabled = prevEnabled
		globalconfig.Config.Csi.StorageClassCreationDisabled = prevDisabled
	})
}

// exports is nil for "no telemetry configured" and non-nil to attach a
// TelemetryConfig carrying exactly those exports.
func skipDefaultFsCluster(skip *bool, exports []weka.TelemetryExport) *weka.WekaCluster {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
	}
	cluster.Spec.Overrides = &weka.WekaClusterSpecOverrides{SkipDefaultFilesystemCreation: skip}
	if exports != nil {
		cluster.Spec.Telemetry = &weka.TelemetryConfig{Exports: exports}
	}
	return cluster
}

func export(sources ...string) []weka.TelemetryExport {
	return []weka.TelemetryExport{{Name: "splunk", Sources: sources}}
}

func TestClusterSkipDefaultFs(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	cases := []struct {
		name                  string
		skip                  *bool
		exports               []weka.TelemetryExport
		csiEnabled            bool
		storageClassesBlocked bool
		wantErrs              int
	}{
		{name: "flag unset", skip: nil, csiEnabled: true, wantErrs: 0},
		{name: "flag false", skip: boolPtr(false), exports: export("audit"), csiEnabled: true, wantErrs: 0},
		{name: "flag true, no dependencies", skip: boolPtr(true), wantErrs: 0},
		{name: "audit source", skip: boolPtr(true), exports: export("audit"), wantErrs: 1},
		// EnsureTelemetry never reads export.Sources - any export configured makes
		// it run the audit calls, so a non-audit source must warn too.
		{name: "non-audit source", skip: boolPtr(true), exports: export("events"), wantErrs: 1},
		{name: "export with no sources", skip: boolPtr(true), exports: export(), wantErrs: 1},
		{name: "telemetry with empty export list", skip: boolPtr(true), exports: []weka.TelemetryExport{}, wantErrs: 0},
		{name: "csi storage classes only", skip: boolPtr(true), csiEnabled: true, wantErrs: 1},
		{name: "csi enabled but storage classes disabled", skip: boolPtr(true), csiEnabled: true, storageClassesBlocked: true, wantErrs: 0},
		{name: "both dependencies", skip: boolPtr(true), exports: export("audit"), csiEnabled: true, wantErrs: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCsi(t, tc.csiEnabled, tc.storageClassesBlocked)

			v := clusterSkipDefaultFs{}
			errs := v.Validate(context.Background(), nil, skipDefaultFsCluster(tc.skip, tc.exports))
			if len(errs) != tc.wantErrs {
				t.Fatalf("expected %d violations, got %d: %v", tc.wantErrs, len(errs), errs)
			}
			for _, e := range errs {
				if e.Field != "spec.overrides.skipDefaultFilesystemCreation" {
					t.Errorf("unexpected field path %q", e.Field)
				}
			}
		})
	}
}

func TestClusterSkipDefaultFs_WrongType(t *testing.T) {
	v := clusterSkipDefaultFs{}
	if errs := v.Validate(context.Background(), nil, &weka.WekaClient{}); errs != nil {
		t.Fatalf("expected nil for non-WekaCluster object, got %v", errs)
	}
}
