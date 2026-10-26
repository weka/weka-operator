package node_agent

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
)

func testOperatorDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "quay-secret"}},
		}}},
	}
}

func defaultSettings() *services.NodeAgentSettings {
	settings := services.DefaultConfigurationSettings().NodeAgent
	return &settings
}

func useDaemonSetConfig(t *testing.T) {
	t.Helper()
	saved := config.Config
	t.Cleanup(func() { config.Config = saved })
	config.Config.NodeAgentDaemonSetName = "weka-operator-node-agent"
	config.Config.OperatorImage = "quay.io/weka.io/weka-operator:v1"
	config.Config.MaintenanceSaName = "weka-operator-maintenance"
	config.Config.Metrics.NodeAgentSecretName = "weka-node-agent-secret"
}

func TestNewNodeAgentDaemonSetDefaults(t *testing.T) {
	useDaemonSetConfig(t)
	ds := NewNodeAgentDaemonSet(defaultSettings(), testOperatorDeployment())

	if ds.Name != "weka-operator-node-agent" || ds.Spec.Selector.MatchLabels["control-plane"] != "weka-node-agent" {
		t.Errorf("unexpected identity: name=%s selector=%v", ds.Name, ds.Spec.Selector.MatchLabels)
	}
	for k, v := range ds.Spec.Selector.MatchLabels {
		if ds.Spec.Template.Labels[k] != v {
			t.Errorf("selector label %s=%s missing from pod labels", k, v)
		}
	}
	if ds.Spec.Template.Labels["app"] != "weka-node-agent" {
		t.Errorf("podmonitor label app missing: %v", ds.Spec.Template.Labels)
	}

	pod := ds.Spec.Template.Spec
	c := pod.Containers[0]
	if c.Name != "node-agent" || c.Image != "quay.io/weka.io/weka-operator:v1" {
		t.Errorf("unexpected container: %s %s", c.Name, c.Image)
	}
	if got := ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable; got.String() != "100%" {
		t.Errorf("maxUnavailable = %s", got.String())
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Errorf("tolerations = %+v", pod.Tolerations)
	}
	if pod.Affinity.NodeAffinity == nil {
		t.Error("default node affinity missing")
	}
	if len(pod.ImagePullSecrets) != 1 || pod.ImagePullSecrets[0].Name != "quay-secret" {
		t.Errorf("imagePullSecrets = %+v", pod.ImagePullSecrets)
	}
	if got := pod.Volumes[2].HostPath.Path; got != "/opt/k8s-weka/shared" {
		t.Errorf("persistence path = %s", got)
	}
	if len(pod.Volumes) != 5 {
		t.Errorf("device-plugins volume present while disabled: %d volumes", len(pod.Volumes))
	}
}

func TestNewNodeAgentDaemonSetDevicePlugin(t *testing.T) {
	useDaemonSetConfig(t)
	settings := defaultSettings()
	settings.DevicePluginEnabled = true
	settings.KubeletPath = "/data/kubelet"
	pod := NewNodeAgentDaemonSet(settings, testOperatorDeployment()).Spec.Template.Spec

	last := pod.Volumes[len(pod.Volumes)-1]
	if last.Name != "device-plugins" || last.HostPath.Path != "/data/kubelet/device-plugins" {
		t.Errorf("device-plugins volume = %+v", last)
	}
}

func TestDaemonSetHashIgnoresMetadataAndTracksSpec(t *testing.T) {
	useDaemonSetConfig(t)
	settings := defaultSettings()
	base := NewNodeAgentDaemonSet(settings, testOperatorDeployment())
	baseHash, err := daemonSetHash(base)
	if err != nil {
		t.Fatal(err)
	}

	maxUnavailable := intstr.FromInt32(1)
	strategyOnly := *settings
	strategyOnly.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
		Type:          appsv1.RollingUpdateDaemonSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable},
	}
	changed := NewNodeAgentDaemonSet(&strategyOnly, testOperatorDeployment())
	changedHash, err := daemonSetHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedHash == baseHash {
		t.Error("updateStrategy change did not alter the hash")
	}
	if !equalPodTemplates(base.Spec.Template, changed.Spec.Template) {
		t.Error("updateStrategy change altered the pod template, which would restart pods")
	}

	base.Labels["extra"] = "x"
	if sameHash, _ := daemonSetHash(base); sameHash != baseHash {
		t.Error("metadata change altered the hash")
	}
}

func equalPodTemplates(a, b corev1.PodTemplateSpec) bool {
	aj, _ := daemonSetHash(&appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: a}})
	bj, _ := daemonSetHash(&appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: b}})
	return aj == bj
}

func TestKeepUnownedMetadata(t *testing.T) {
	existing := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Labels:      map[string]string{"foreign": "label", "app": "old"},
		Annotations: map[string]string{"foreign": "annotation", daemonSetHashAnnotation: "old"},
	}}
	existing.Spec.Template.Annotations = map[string]string{"kubectl.kubernetes.io/restartedAt": "now", "prometheus.io/port": "1"}
	existing.Spec.Template.Labels = map[string]string{"foreign": "template-label", "app": "old"}

	target := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Labels:      map[string]string{"app": "new"},
		Annotations: map[string]string{daemonSetHashAnnotation: "new"},
	}}
	target.Spec.Template.Annotations = map[string]string{"prometheus.io/port": "2"}
	target.Spec.Template.Labels = map[string]string{"app": "weka-node-agent"}

	keepUnownedMetadata(existing, target)

	if target.Labels["foreign"] != "label" || target.Labels["app"] != "new" {
		t.Errorf("labels = %v", target.Labels)
	}
	if target.Annotations["foreign"] != "annotation" || target.Annotations[daemonSetHashAnnotation] != "new" {
		t.Errorf("annotations = %v", target.Annotations)
	}
	want := map[string]string{"kubectl.kubernetes.io/restartedAt": "now", "prometheus.io/port": "2"}
	if !reflect.DeepEqual(target.Spec.Template.Annotations, want) {
		t.Errorf("template annotations = %v, want %v", target.Spec.Template.Annotations, want)
	}
	wantLabels := map[string]string{"foreign": "template-label", "app": "weka-node-agent"}
	if !reflect.DeepEqual(target.Spec.Template.Labels, wantLabels) {
		t.Errorf("template labels = %v, want %v", target.Spec.Template.Labels, wantLabels)
	}
}
