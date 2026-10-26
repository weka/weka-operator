package resources

import (
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
)

func TestPodFactoryKeepsConfiguredDNSPolicyAndServiceAccount(t *testing.T) {
	original := config.Config.DNSPolicy.HostNetwork
	config.Config.DNSPolicy.HostNetwork = string(corev1.DNSDefault)
	defer func() { config.Config.DNSPolicy.HostNetwork = original }()

	pod := mustPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
		spec.ServiceAccountName = "weka-sa"
	})

	if pod.Spec.DNSPolicy != corev1.DNSDefault {
		t.Errorf("dnsPolicy = %q, want %q", pod.Spec.DNSPolicy, corev1.DNSDefault)
	}
	if pod.Spec.ServiceAccountName != "weka-sa" {
		t.Errorf("serviceAccountName = %q, want %q", pod.Spec.ServiceAccountName, "weka-sa")
	}
}
