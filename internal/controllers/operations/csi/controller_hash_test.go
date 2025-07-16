package csi

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
)

func TestGetCsiControllerDeploymentHash(t *testing.T) {
	// Setup config
	config.Config.CsiImage = "test-csi-image"
	config.Config.CsiAttacherImage = "test-attacher-image"
	config.Config.CsiProvisionerImage = "test-provisioner-image"
	config.Config.CsiResizerImage = "test-resizer-image"
	config.Config.CsiSnapshotterImage = "test-snapshotter-image"
	config.Config.CsiLivenessProbeImage = "test-liveness-image"

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
					EnforceSecureHttps:    true,
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
	wekaClient2.Spec.CsiConfig.Advanced.EnforceSecureHttps = false

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

func TestGetCsiControllerDeploymentHashWithoutAdvancedConfig(t *testing.T) {
	// Setup config
	config.Config.CsiImage = "test-csi-image"
	config.Config.CsiAttacherImage = "test-attacher-image"
	config.Config.CsiProvisionerImage = "test-provisioner-image"
	config.Config.CsiResizerImage = "test-resizer-image"
	config.Config.CsiSnapshotterImage = "test-snapshotter-image"
	config.Config.CsiLivenessProbeImage = "test-liveness-image"

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
