package resources

import (
	"os"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// Helm always sets WEKA_POD_RUNTIME_IMAGE on the manager; pods built without it are rejected.
func TestMain(m *testing.M) {
	config.Config.WekaPodRuntimeImage = "weka-pod-runtime:test"
	os.Exit(m.Run())
}

func setRuntimeConfig(t *testing.T, global bool, modes ...string) {
	t.Helper()
	origImage, origGlobal, origModes := config.Config.WekaPodRuntimeImage, config.Config.UsePythonFallback, config.Config.PythonFallbackModes
	origOtel := config.Config.Otel.PythonPackagesInstallerImage
	t.Cleanup(func() {
		config.Config.WekaPodRuntimeImage, config.Config.UsePythonFallback, config.Config.PythonFallbackModes = origImage, origGlobal, origModes
		config.Config.Otel.PythonPackagesInstallerImage = origOtel
	})
	config.Config.WekaPodRuntimeImage = "weka-pod-runtime:latest"
	origPolicy := config.Config.WekaPodRuntimeImagePullPolicy
	t.Cleanup(func() { config.Config.WekaPodRuntimeImagePullPolicy = origPolicy })
	config.Config.WekaPodRuntimeImagePullPolicy = string(corev1.PullAlways)
	config.Config.Otel.PythonPackagesInstallerImage = "otel-packages:latest"
	config.Config.UsePythonFallback = global
	config.Config.PythonFallbackModes = map[string]bool{}
	for _, m := range modes {
		config.Config.PythonFallbackModes[m] = true
	}
}

func assertRuntime(t *testing.T, pod *corev1.Pod, wantPython bool) {
	t.Helper()
	const data, installer, otel = "weka-pod-runtime-data", "weka-pod-runtime-installer", "otel-packages-installer"

	wantCmd := []string{"/weka-pod-runtime-data/weka-pod-runtime"}
	if wantPython {
		wantCmd = []string{"python3", "/opt/weka_runtime.py"}
	}
	if got := pod.Spec.Containers[0].Command; !slices.Equal(got, wantCmd) {
		t.Errorf("command = %v, want %v", got, wantCmd)
	}

	hasInit := func(name string) bool {
		return slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool { return c.Name == name })
	}
	hasVolume := slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == data })
	hasMount := slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == data })

	if hasInit(installer) == wantPython {
		t.Errorf("%s present = %v, want %v", installer, hasInit(installer), !wantPython)
	}
	for _, c := range pod.Spec.InitContainers {
		if c.Name == installer && c.ImagePullPolicy != corev1.PullAlways {
			t.Errorf("%s pull policy = %q, want %q", installer, c.ImagePullPolicy, corev1.PullAlways)
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if cpu := c.Resources.Requests[corev1.ResourceCPU]; cpu.MilliValue()%1000 == 0 {
			t.Errorf("%s cpu request = %s: a whole-core init container takes exclusive CPUs and fails full-pcpus-only admission on SMT nodes", c.Name, cpu.String())
		}
	}
	for _, c := range slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers) {
		for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			req, lim := c.Resources.Requests[r], c.Resources.Limits[r]
			if req.IsZero() || req.Cmp(lim) != 0 {
				t.Errorf("%s %s request = %s, limit = %s: must be set and equal or the pod loses Guaranteed QoS", c.Name, r, req.String(), lim.String())
			}
		}
	}
	if hasVolume == wantPython || hasMount == wantPython {
		t.Errorf("%s volume = %v, mount = %v, want both %v", data, hasVolume, hasMount, !wantPython)
	}
	if hasInit(otel) != wantPython {
		t.Errorf("%s present = %v, want %v", otel, hasInit(otel), wantPython)
	}
}

func TestPodRuntimeSelection(t *testing.T) {
	tests := []struct {
		name       string
		global     bool
		modes      []string
		mode       string
		wantPython bool
	}{
		{name: "unlisted mode stays on Go", modes: []string{weka.WekaContainerModeClient}, mode: weka.WekaContainerModeCompute},
		{name: "listed mode uses Python", modes: []string{weka.WekaContainerModeClient}, mode: weka.WekaContainerModeClient, wantPython: true},
		{name: "global switch covers every mode", global: true, mode: weka.WekaContainerModeCompute, wantPython: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRuntimeConfig(t, tt.global, tt.modes...)
			pod, err := createTestPod(t, tt.mode, nil)
			if err != nil {
				t.Fatalf("Create returned unexpected error: %v", err)
			}
			assertRuntime(t, pod, tt.wantPython)
		})
	}
}

func TestPodRuntimeImageRequiredForGoRuntime(t *testing.T) {
	setRuntimeConfig(t, false, weka.WekaContainerModeClient)
	config.Config.WekaPodRuntimeImage = ""

	if _, err := createTestPod(t, weka.WekaContainerModeCompute, nil); err == nil {
		t.Error("Go-runtime pod without WEKA_POD_RUNTIME_IMAGE: want error, got nil")
	}
	if _, err := createTestPod(t, weka.WekaContainerModeClient, nil); err != nil {
		t.Errorf("Python-fallback pod without WEKA_POD_RUNTIME_IMAGE: unexpected error %v", err)
	}
}

func TestDedicatedPodExpectsExclusiveCPUs(t *testing.T) {
	setRuntimeConfig(t, false)
	pod, err := createTestPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) {
		s.CpuPolicy = weka.CpuPolicyDedicated
	})
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	if !ExpectsExclusiveCPUs(pod) {
		t.Error("dedicated pod: ExpectsExclusiveCPUs = false, want true")
	}
}
