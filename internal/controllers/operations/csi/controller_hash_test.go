package csi

import (
	"context"
	"slices"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
)

func TestGetCsiControllerDeploymentHash(t *testing.T) {
	// Setup config
	config.Config.Csi.WekafsImage = "test-csi-image"
	config.Config.Csi.AttacherImage = "test-attacher-image"
	config.Config.Csi.ProvisionerImage = "test-provisioner-image"
	config.Config.Csi.ResizerImage = "test-resizer-image"
	config.Config.Csi.SnapshotterImage = "test-snapshotter-image"
	config.Config.Csi.LivenessProbeImage = "test-liveness-image"

	// Create test WekaClient
	wekaClient := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"test-label": "test-value",
			},
		},
		Spec: weka.WekaClientSpec{
			NodeSelector: map[string]string{
				"kubernetes.io/os": "linux",
			},
			RawTolerations: []corev1.Toleration{
				{
					Key:    "test-key",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			CsiConfig: &weka.ClientCsiConfig{
				Advanced: &weka.AdvancedCsiConfig{
					ControllerLabels: map[string]string{
						"csi-label": "csi-value",
					},
					ControllerTolerations: []corev1.Toleration{
						{
							Key:    "csi-key",
							Value:  "csi-value",
							Effect: corev1.TaintEffectNoExecute,
						},
					},
					EnforceTrustedHttps:   true,
					SkipGarbageCollection: false,
				},
			},
		},
	}

	// Test hash generation
	hash1, err := GetCsiControllerDeploymentHash("test-group", wekaClient)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == "" {
		t.Fatal("Hash should not be empty")
	}

	// Test hash consistency - same input should produce same hash
	hash2, err := GetCsiControllerDeploymentHash("test-group", wekaClient)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 != hash2 {
		t.Fatalf("Hash should be consistent: %s != %s", hash1, hash2)
	}

	// Test hash difference - different input should produce different hash
	wekaClient2 := wekaClient.DeepCopy()
	wekaClient2.Spec.CsiConfig.Advanced.EnforceTrustedHttps = false

	hash3, err := GetCsiControllerDeploymentHash("test-group", wekaClient2)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash1 == hash3 {
		t.Fatal("Different configurations should produce different hashes")
	}

	t.Logf("Hash 1: %s", hash1)
	t.Logf("Hash 2: %s", hash2)
	t.Logf("Hash 3: %s", hash3)
}

func TestGetCsiControllerDeploymentHashHealthMonitor(t *testing.T) {
	config.Config.Csi.WekafsImage = "test-csi-image"
	config.Config.Csi.HealthMonitorImage = "test-health-monitor-image"
	config.Config.Csi.HealthMonitor = config.CsiHealthMonitorSettings{
		Enabled:         true,
		MonitorInterval: "5m",
		TimeoutSeconds:  300,
	}
	t.Cleanup(func() { config.Config.Csi.HealthMonitor = config.CsiHealthMonitorSettings{} })

	wekaClient := &weka.WekaClient{}

	enabledHash, err := GetCsiControllerDeploymentHash("test-group", wekaClient)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	// Toggling the sidecar off must roll the deployment: it removes a container and
	// flips --advertisevolumehealthsupport on the driver.
	config.Config.Csi.HealthMonitor.Enabled = false
	disabledHash, err := GetCsiControllerDeploymentHash("test-group", wekaClient)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if enabledHash == disabledHash {
		t.Fatal("Enabling/disabling the health monitor should change the deployment hash")
	}

	// Retuning the sweep must roll the deployment too, since the interval is a container arg
	config.Config.Csi.HealthMonitor.Enabled = true
	config.Config.Csi.HealthMonitor.MonitorInterval = "10m"
	retunedHash, err := GetCsiControllerDeploymentHash("test-group", wekaClient)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}
	if retunedHash == enabledHash {
		t.Fatal("Changing the monitor interval should change the deployment hash")
	}
}

func TestNewCsiControllerDeploymentHealthMonitor(t *testing.T) {
	config.Config.Csi.WekafsImage = "test-csi-image"
	config.Config.Csi.HealthMonitorImage = "test-health-monitor-image"
	t.Cleanup(func() { config.Config.Csi.HealthMonitor = config.CsiHealthMonitorSettings{} })

	const containerName = "csi-external-health-monitor-controller"

	findContainer := func(t *testing.T, deployment *appsv1.Deployment) *corev1.Container {
		t.Helper()
		for i, c := range deployment.Spec.Template.Spec.Containers {
			if c.Name == containerName {
				return &deployment.Spec.Template.Spec.Containers[i]
			}
		}
		return nil
	}

	driverArgs := func(t *testing.T, deployment *appsv1.Deployment) []string {
		t.Helper()
		for _, c := range deployment.Spec.Template.Spec.Containers {
			if c.Name == "wekafs" {
				return c.Args
			}
		}
		t.Fatal("wekafs container not found")
		return nil
	}

	t.Run("enabled", func(t *testing.T) {
		config.Config.Csi.HealthMonitor = config.CsiHealthMonitorSettings{
			Enabled:         true,
			MonitorInterval: "5m",
			TimeoutSeconds:  300,
		}

		deployment, err := NewCsiControllerDeployment(context.Background(), "test-group", &weka.WekaClient{})
		if err != nil {
			t.Fatalf("Failed to build deployment: %v", err)
		}

		container := findContainer(t, deployment)
		if container == nil {
			t.Fatalf("%s container should be present when the health monitor is enabled", containerName)
		}
		if container.Image != "test-health-monitor-image" {
			t.Errorf("image = %q, want %q", container.Image, "test-health-monitor-image")
		}

		wantArgs := []string{"--timeout=300s", "--monitor-interval=5m", "--list-volumes-interval=5m"}
		for _, want := range wantArgs {
			if !slices.Contains(container.Args, want) {
				t.Errorf("sidecar args %v missing %q", container.Args, want)
			}
		}

		// The driver only advertises VOLUME_CONDITION when something consumes it
		if args := driverArgs(t, deployment); !slices.Contains(args, "--advertisevolumehealthsupport=true") {
			t.Errorf("driver args %v missing --advertisevolumehealthsupport=true", args)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		config.Config.Csi.HealthMonitor = config.CsiHealthMonitorSettings{Enabled: false}

		deployment, err := NewCsiControllerDeployment(context.Background(), "test-group", &weka.WekaClient{})
		if err != nil {
			t.Fatalf("Failed to build deployment: %v", err)
		}

		if findContainer(t, deployment) != nil {
			t.Errorf("%s container should be absent when the health monitor is disabled", containerName)
		}
		if args := driverArgs(t, deployment); !slices.Contains(args, "--advertisevolumehealthsupport=false") {
			t.Errorf("driver args %v missing --advertisevolumehealthsupport=false", args)
		}
	})
}

func TestGetCsiControllerDeploymentHashWithoutAdvancedConfig(t *testing.T) {
	// Setup config
	config.Config.Csi.WekafsImage = "test-csi-image"
	config.Config.Csi.AttacherImage = "test-attacher-image"
	config.Config.Csi.ProvisionerImage = "test-provisioner-image"
	config.Config.Csi.ResizerImage = "test-resizer-image"
	config.Config.Csi.SnapshotterImage = "test-snapshotter-image"
	config.Config.Csi.LivenessProbeImage = "test-liveness-image"

	// Create test WekaClient without advanced CSI config
	wekaClient := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"test-label": "test-value",
			},
		},
		Spec: weka.WekaClientSpec{
			NodeSelector: map[string]string{
				"kubernetes.io/os": "linux",
			},
			RawTolerations: []corev1.Toleration{
				{
					Key:    "test-key",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	// Test hash generation
	hash, err := GetCsiControllerDeploymentHash("test-group", wekaClient)
	if err != nil {
		t.Fatalf("Failed to generate hash: %v", err)
	}

	if hash == "" {
		t.Fatal("Hash should not be empty")
	}

	t.Logf("Hash without advanced config: %s", hash)
}
