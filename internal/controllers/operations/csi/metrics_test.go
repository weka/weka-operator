package csi

import (
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/services"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

// Disabling metrics must clear every site that opens or advertises a metrics port, not just the
// flag: under hostNetwork the ports are host ports, which is the reason to turn them off.
func TestNewCsiNodeDaemonSet_MetricsDisabled(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi
	settings.MetricsEnabled = false

	ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", testWekaClient(nil), "clients", "default", nil, settings)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}

	template := ds.Spec.Template
	for key := range template.Annotations {
		if strings.HasPrefix(key, "prometheus.io/") {
			t.Errorf("unexpected scrape annotation %q", key)
		}
	}

	plugin := template.Spec.Containers[0]
	for _, arg := range plugin.Args {
		if strings.HasPrefix(arg, "--enablemetrics") || strings.HasPrefix(arg, "--metricsport") {
			t.Errorf("unexpected metrics arg %q", arg)
		}
	}
	assertNoMetricsPort(t, plugin)
}

func TestNewCsiNodeDaemonSet_MetricsEnabledByDefault(t *testing.T) {
	ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", testWekaClient(nil), "clients", "default", nil, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}

	template := ds.Spec.Template
	if template.Annotations["prometheus.io/port"] != "9094" {
		t.Errorf("scrape port = %q, want 9094", template.Annotations["prometheus.io/port"])
	}

	plugin := template.Spec.Containers[0]
	if !hasArg(plugin.Args, "--enablemetrics") {
		t.Error("missing --enablemetrics")
	}
	if !hasArg(plugin.Args, "--metricsport=9094") {
		t.Errorf("missing --metricsport=9094, got %v", plugin.Args)
	}
	if !hasPort(plugin.Ports, NodeMetricsPort) {
		t.Errorf("missing metrics container port, got %+v", plugin.Ports)
	}
}

// The sidecars bind their own metrics ports, so they have to be gated along with the plugin.
func TestNewCsiControllerDeployment_MetricsDisabledIncludingSidecars(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi
	settings.MetricsEnabled = false

	deployment, err := NewCsiControllerDeployment(t.Context(), "csi", testWekaClient(nil), settings)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}

	template := deployment.Spec.Template
	for key := range template.Annotations {
		if strings.HasPrefix(key, "prometheus.io/") {
			t.Errorf("unexpected scrape annotation %q", key)
		}
	}

	for _, container := range template.Spec.Containers {
		for _, arg := range container.Args {
			if strings.HasPrefix(arg, "--enablemetrics") ||
				strings.HasPrefix(arg, "--metricsport") ||
				strings.HasPrefix(arg, "--http-endpoint") {
				t.Errorf("container %s: unexpected metrics arg %q", container.Name, arg)
			}
		}
		assertNoMetricsPort(t, container)
	}
}

func TestGetCsiControllerDeploymentHash_ChangesWithMetrics(t *testing.T) {
	client := testWekaClient(nil)

	enabled, err := GetCsiControllerDeploymentHash("csi", client, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("hash with metrics enabled: %v", err)
	}

	settings := services.DefaultConfigurationSettings().Csi
	settings.MetricsEnabled = false
	disabled, err := GetCsiControllerDeploymentHash("csi", client, settings)
	if err != nil {
		t.Fatalf("hash with metrics disabled: %v", err)
	}

	if enabled == disabled {
		t.Error("hash did not change when metrics were disabled, existing deployments would not roll")
	}
}

func assertNoMetricsPort(t *testing.T, container corev1.Container) {
	t.Helper()
	for _, port := range container.Ports {
		if strings.Contains(port.Name, "metrics") {
			t.Errorf("container %s: unexpected port %s/%d", container.Name, port.Name, port.ContainerPort)
		}
	}
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasPort(ports []corev1.ContainerPort, want int) bool {
	for _, port := range ports {
		if port.ContainerPort == int32(want) {
			return true
		}
	}
	return false
}

func TestNewCsiDriver_UsesGivenFsGroupPolicy(t *testing.T) {
	for _, want := range []storagev1.FSGroupPolicy{
		storagev1.FileFSGroupPolicy,
		storagev1.NoneFSGroupPolicy,
		storagev1.ReadWriteOnceWithFSTypeFSGroupPolicy,
	} {
		driver := NewCsiDriver("csi.weka.io", want)
		if driver.Spec.FSGroupPolicy == nil || *driver.Spec.FSGroupPolicy != want {
			t.Errorf("FSGroupPolicy = %v, want %v", driver.Spec.FSGroupPolicy, want)
		}
	}
}

// The value a configuration policy sets has to survive the whole path onto the object, not just
// the mapping into settings.
func TestNewCsiDriver_TakesFsGroupPolicyFromSettings(t *testing.T) {
	none := "None"
	settings := services.SettingsFromPayload(&weka.ConfigurationPayload{
		Csi: &weka.CsiSpec{FsGroupPolicy: &none},
	}).Csi

	driver := NewCsiDriver("csi.weka.io", settings.FsGroupPolicy)
	if driver.Spec.FSGroupPolicy == nil || *driver.Spec.FSGroupPolicy != storagev1.NoneFSGroupPolicy {
		t.Errorf("FSGroupPolicy = %v, want %v", driver.Spec.FSGroupPolicy, storagev1.NoneFSGroupPolicy)
	}
}
