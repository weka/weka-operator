package resources

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func createPodWithDTK(t *testing.T, mode, dtkImage string) *corev1.Pod {
	t.Helper()
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-container", UID: "test-uid"},
		Spec: weka.WekaContainerSpec{
			Mode:      mode,
			Image:     "test-image:latest",
			NumCores:  1,
			CpuPolicy: weka.CpuPolicyAuto,
			Hugepages: 4000,
		},
	}
	if dtkImage != "" {
		payload, err := json.Marshal(map[string]string{
			"targetImage":        "test-image:latest",
			"cliImage":           "test-cli:latest",
			"driverToolkitImage": dtkImage,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		container.Spec.Instructions = &weka.Instructions{
			Type:    weka.InstructionCopyWekaFilesToDriverLoader,
			Payload: string(payload),
		}
	}
	factory := NewPodFactory(container, &discovery.DiscoveryNodeInfo{}, &domain.FeatureFlags{}, services.SchedulingSettings{})
	pod, err := factory.Create(context.Background(), nil)
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return pod
}

func findInitContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == name {
			return &pod.Spec.InitContainers[i]
		}
	}
	return nil
}

func findVolume(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

func findMount(c *corev1.Container, name string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == name {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}

func TestDriverToolkitInitContainer(t *testing.T) {
	const dtk = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:abc"

	t.Run("drivers-builder with DTK image", func(t *testing.T) {
		pod := createPodWithDTK(t, weka.WekaContainerModeDriversBuilder, dtk)

		init := findInitContainer(pod, "driver-toolkit")
		if init == nil {
			t.Fatalf("driver-toolkit init container missing")
		}
		if init.Image != dtk {
			t.Errorf("init image = %q, want %q", init.Image, dtk)
		}
		if !reflect.DeepEqual(init.Resources, HelperContainerResources()) {
			t.Errorf("init resources = %v, want helper resources", init.Resources)
		}
		if m := findMount(init, "usrsrc"); m == nil || m.MountPath != "/shared-usr-src" {
			t.Errorf("init container usrsrc mount = %+v, want /shared-usr-src", m)
		}
		if m := findMount(&pod.Spec.Containers[0], "usrsrc"); m == nil || m.MountPath != "/usr/src" {
			t.Errorf("main container usrsrc mount = %+v, want /usr/src", m)
		}
		if v := findVolume(pod, "usrsrc"); v == nil || v.EmptyDir == nil {
			t.Errorf("usrsrc volume = %+v, want emptyDir", v)
		}
		if v := findVolume(pod, "libmodules"); v == nil || v.HostPath == nil || v.HostPath.Path != "/lib/modules" {
			t.Errorf("libmodules volume = %+v, want hostPath /lib/modules", v)
		}
		if findInitContainer(pod, "copy-cli") == nil || findInitContainer(pod, "copy-weka-version") == nil {
			t.Errorf("copy-cli/copy-weka-version init containers missing")
		}
		if findVolume(pod, "dtk") != nil {
			t.Errorf("unexpected dtk volume")
		}
	})

	t.Run("drivers-loader without DTK image", func(t *testing.T) {
		pod := createPodWithDTK(t, weka.WekaContainerModeDriversLoader, "")
		if findInitContainer(pod, "driver-toolkit") != nil {
			t.Errorf("unexpected driver-toolkit init container")
		}
		if v := findVolume(pod, "usrsrc"); v == nil || v.HostPath == nil || v.HostPath.Path != "/usr/src" {
			t.Errorf("usrsrc volume = %+v, want hostPath /usr/src", v)
		}
	})
}
