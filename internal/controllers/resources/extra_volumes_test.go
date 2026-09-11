package resources

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// rawVolumes marshals volumes into the RawExtension shape ExtraVolumes expects.
func rawVolumes(t *testing.T, volumes []corev1.Volume) *runtime.RawExtension {
	t.Helper()
	data, err := json.Marshal(volumes)
	if err != nil {
		t.Fatalf("failed to marshal test volumes: %v", err)
	}
	return &runtime.RawExtension{Raw: data}
}

// createTestPod builds a container with the given mode/extras and runs it through the real
// PodFactory.Create pipeline, so collision checks exercise the pod's actual, final volume set
// (including mode- and config-dependent volumes) rather than a hand-picked approximation of it.
//
// ConfigureEnv (the normal source of config.Config defaults, e.g. Smbw.ShmSize) reads required
// env vars and is not meant to run in a unit test, so smbw-mode callers must set
// config.Config.Smbw.ShmSize themselves.
func createTestPod(t *testing.T, mode string, mutate func(*weka.WekaContainerSpec)) (*corev1.Pod, error) {
	t.Helper()
	return createTestPodOnNode(t, mode, &discovery.DiscoveryNodeInfo{}, mutate)
}

// createTestPodOnNode is createTestPod with an explicit node info, for cases (COS driver
// dependencies) that branch on it.
func createTestPodOnNode(t *testing.T, mode string, nodeInfo *discovery.DiscoveryNodeInfo, mutate func(*weka.WekaContainerSpec)) (*corev1.Pod, error) {
	t.Helper()
	spec := weka.WekaContainerSpec{
		Mode:      mode,
		Image:     "test-image:latest",
		NumCores:  1,
		CpuPolicy: weka.CpuPolicyAuto,
		Hugepages: 4000,
	}
	if mutate != nil {
		mutate(&spec)
	}
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-container", UID: "test-uid"},
		Spec:       spec,
	}
	factory := NewPodFactory(container, nodeInfo, &domain.FeatureFlags{})
	return factory.Create(context.Background(), nil)
}

func TestApplyExtraVolumes_AppendedAfterBaseVolumesAndMainContainerOnly(t *testing.T) {
	// Force an init container (otel-packages-installer) into the pod so we can assert extras
	// never leak into it.
	original := config.Config.Otel.PythonPackagesInstallerImage
	config.Config.Otel.PythonPackagesInstallerImage = "otel-installer:latest"
	defer func() { config.Config.Otel.PythonPackagesInstallerImage = original }()

	pod, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
		spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
			{Name: "my-extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		})
		spec.ExtraVolumeMounts = []corev1.VolumeMount{
			{Name: "my-extra", MountPath: "/mnt/my-extra"},
		}
	})
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	if len(pod.Spec.InitContainers) == 0 {
		t.Fatalf("expected at least one init container (otel-packages-installer) in this fixture")
	}

	lastVolume := pod.Spec.Volumes[len(pod.Spec.Volumes)-1]
	if lastVolume.Name != "my-extra" {
		t.Errorf("extra volume was not appended after base volumes: last volume is %q", lastVolume.Name)
	}

	lastMount := pod.Spec.Containers[0].VolumeMounts[len(pod.Spec.Containers[0].VolumeMounts)-1]
	if lastMount.Name != "my-extra" || lastMount.MountPath != "/mnt/my-extra" {
		t.Errorf("extra mount not appended to main container as expected, got %+v", lastMount)
	}

	for _, ic := range pod.Spec.InitContainers {
		for _, m := range ic.VolumeMounts {
			if m.Name == "my-extra" {
				t.Errorf("extra volume mount leaked into init container %q", ic.Name)
			}
		}
	}
}

func TestApplyExtraVolumes_CollisionErrors(t *testing.T) {
	t.Run("plain reserved name", func(t *testing.T) {
		_, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
			spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
				{Name: "dev", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			})
		})
		if err == nil {
			t.Fatal("expected error for reserved volume name \"dev\", got nil")
		}
	})

	t.Run("mode-dependent name: smbw-shm on an smbw container", func(t *testing.T) {
		originalShmSize := config.Config.Smbw.ShmSize
		config.Config.Smbw.ShmSize = "8Gi"
		defer func() { config.Config.Smbw.ShmSize = originalShmSize }()

		_, err := createTestPod(t, weka.WekaContainerModeSmbw, func(spec *weka.WekaContainerSpec) {
			spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
				{Name: "smbw-shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			})
		})
		if err == nil {
			t.Fatal("expected error for smbw-shm colliding on an smbw container, got nil")
		}
	})

	t.Run("config-dependent name: otel-packages with installer image configured", func(t *testing.T) {
		original := config.Config.Otel.PythonPackagesInstallerImage
		config.Config.Otel.PythonPackagesInstallerImage = "otel-installer:latest"
		defer func() { config.Config.Otel.PythonPackagesInstallerImage = original }()

		_, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
			spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
				{Name: "otel-packages", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			})
		})
		if err == nil {
			t.Fatal("expected error for otel-packages colliding when the installer image is configured, got nil")
		}
	})

	t.Run("AdditionalSecrets-derived name", func(t *testing.T) {
		_, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
			spec.AdditionalSecrets = map[string]string{"wekahome-cacert": "wekahome-cacert-secret-name"}
			spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
				{Name: "wekahome-cacert-secret", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			})
		})
		if err == nil {
			t.Fatal("expected error for wekahome-cacert-secret colliding with the AdditionalSecrets-derived volume, got nil")
		}
	})

	t.Run("reserved mount path", func(t *testing.T) {
		_, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
			spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
				{Name: "my-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			})
			spec.ExtraVolumeMounts = []corev1.VolumeMount{
				{Name: "my-vol", MountPath: "/opt/weka/foo"},
			}
		})
		if err == nil {
			t.Fatal("expected error for mount path under reserved /opt/weka, got nil")
		}
	})
}

func TestApplyExtraVolumes_MountReferencingUndeclaredVolumeErrors(t *testing.T) {
	_, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
		spec.ExtraVolumeMounts = []corev1.VolumeMount{
			{Name: "never-declared", MountPath: "/mnt/never-declared"},
		}
	})
	if err == nil {
		t.Fatal("expected error for a mount naming no declared extraVolumes entry, got nil")
	}
}

func TestApplyExtraVolumes_EtcSslCertsAccepted(t *testing.T) {
	pod, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
		spec.ExtraVolumes = rawVolumes(t, []corev1.Volume{
			{Name: "ca-bundle", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		})
		spec.ExtraVolumeMounts = []corev1.VolumeMount{
			{Name: "ca-bundle", MountPath: "/etc/ssl/certs"},
		}
	})
	if err != nil {
		t.Fatalf("expected /etc/ssl/certs mount to be accepted, got error: %v", err)
	}

	found := false
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == "ca-bundle" && m.MountPath == "/etc/ssl/certs" {
			found = true
		}
	}
	if !found {
		t.Error("expected ca-bundle mount at /etc/ssl/certs on the main container, not found")
	}
}

func TestNormalizeExtraVolumes_UnsetNullEmptyCollapseToNil(t *testing.T) {
	cases := []struct {
		name string
		raw  *runtime.RawExtension
	}{
		{"nil", nil},
		{"nil Raw bytes", &runtime.RawExtension{}},
		{"JSON null", &runtime.RawExtension{Raw: []byte("null")}},
		{"empty array", &runtime.RawExtension{Raw: []byte("[]")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeExtraVolumes(c.raw); got != nil {
				t.Errorf("NormalizeExtraVolumes(%s) = %+v, want nil", c.name, got)
			}
			if got := ExtraVolumesDigest(c.raw); got != "" {
				t.Errorf("ExtraVolumesDigest(%s) = %q, want \"\"", c.name, got)
			}
		})
	}
}

// TestNormalizeExtraVolumes_MalformedInputSurvives guards the reject-don't-skip guarantee
// against a structurally invalid extraVolumes value (a map where a list belongs), not merely an
// unknown-field typo — json.Unmarshal ignores unknown fields regardless, so catching those is
// admission's DisallowUnknownFields job, not this function's. Returning nil here would look
// tidy — "couldn't parse it, so there's nothing to normalize" — but nil is exactly what an
// admission-disabled cluster's WekaContainer spec would then carry forward: the pod factory
// would silently build a pod without the user's volumes instead of erroring. Preserving the
// bytes keeps them reachable by GetExtraVolumes on the pod-build path, where the same malformed
// input is turned into a real, visible error (see TestApplyExtraVolumes_MalformedInputErrors).
func TestNormalizeExtraVolumes_MalformedInputSurvives(t *testing.T) {
	malformed := &runtime.RawExtension{Raw: []byte(`{"not":"a list"}`)}
	inputCopy := append([]byte(nil), malformed.Raw...)

	normalized := NormalizeExtraVolumes(malformed)
	if normalized == nil {
		t.Fatal("NormalizeExtraVolumes(malformed) = nil, want the input preserved (not dropped)")
	}

	digest := ExtraVolumesDigest(malformed)
	if digest == "" {
		t.Fatal("ExtraVolumesDigest(malformed) = \"\", want a non-empty digest distinct from an empty spec")
	}
	if emptyDigest := ExtraVolumesDigest(nil); digest == emptyDigest {
		t.Errorf("ExtraVolumesDigest(malformed) = %q, collides with the empty-spec digest %q", digest, emptyDigest)
	}

	// The error path must not alias the caller's slice either: mutating the result must not
	// mutate malformed.Raw.
	normalized.Raw[0] = 'X'
	if string(malformed.Raw) != string(inputCopy) {
		t.Fatal("mutating the result of NormalizeExtraVolumes(malformed) mutated the caller's input")
	}
}

// TestApplyExtraVolumes_MalformedInputErrors is the half that actually matters: it proves the
// error surfaces on the path that builds real pods, so the backstop still fires for a user
// running with enableAdmissionControl: false and no other layer to catch a structurally invalid
// extraVolumes value.
func TestApplyExtraVolumes_MalformedInputErrors(t *testing.T) {
	malformed := &runtime.RawExtension{Raw: []byte(`{"not":"a list"}`)}

	_, err := createTestPod(t, weka.WekaContainerModeCompute, func(spec *weka.WekaContainerSpec) {
		spec.ExtraVolumes = malformed
	})
	if err == nil {
		t.Fatal("expected applyExtraVolumes (via Create) to error on malformed extraVolumes, got nil")
	}
}

// TestNormalizeExtraVolumes_UnknownFieldSurvives is Fix C's guard: strict decoding now treats an
// unknown field as a decode failure (same branch as MalformedInputSurvives above), so it is
// preserved as-is instead of silently dropped through a lenient re-marshal — the CRD's
// PreserveUnknownFields means the API server accepted that field legitimately.
func TestNormalizeExtraVolumes_UnknownFieldSurvives(t *testing.T) {
	raw := &runtime.RawExtension{Raw: []byte(`[{"name":"v1","emptyDir":{},"unknownField":"x"}]`)}

	normalized := NormalizeExtraVolumes(raw)
	if normalized == nil {
		t.Fatal("NormalizeExtraVolumes(unknown field) = nil, want the input preserved (not dropped)")
	}
	if string(normalized.Raw) != string(raw.Raw) {
		t.Errorf("NormalizeExtraVolumes(unknown field) = %s, want raw bytes preserved unchanged: %s", normalized.Raw, raw.Raw)
	}
}

func TestExtraVolumeMountsDigest_EmptyAndNil(t *testing.T) {
	if got := ExtraVolumeMountsDigest(nil); got != "" {
		t.Errorf("ExtraVolumeMountsDigest(nil) = %q, want \"\"", got)
	}
	if got := ExtraVolumeMountsDigest([]corev1.VolumeMount{}); got != "" {
		t.Errorf("ExtraVolumeMountsDigest(empty) = %q, want \"\"", got)
	}
}

func TestExtraVolumesDigest_WhitespaceAndKeyOrderAreIgnored(t *testing.T) {
	compact := &runtime.RawExtension{Raw: []byte(
		`[{"name":"v1","emptyDir":{}}]`,
	)}
	reordered := &runtime.RawExtension{Raw: []byte(`
		[
			{
				"emptyDir": {},
				"name": "v1"
			}
		]
	`)}

	got1 := ExtraVolumesDigest(compact)
	got2 := ExtraVolumesDigest(reordered)
	if got1 == "" {
		t.Fatal("expected a non-empty digest")
	}
	if got1 != got2 {
		t.Errorf("digest changed under whitespace/key-order variation: %q != %q", got1, got2)
	}
}

// TestExtraVolumesDigest_EmptyDirSizeLimitChangeIsDetected is the exact case gob-based
// util.HashStruct would miss: resource.Quantity keeps its value in unexported fields, so gob
// encodes every Quantity identically regardless of its value. The JSON-based digest must not
// share that blind spot.
func TestExtraVolumesDigest_EmptyDirSizeLimitChangeIsDetected(t *testing.T) {
	base := &runtime.RawExtension{Raw: []byte(
		`[{"name":"v1","emptyDir":{"sizeLimit":"1Gi"}}]`,
	)}
	changed := &runtime.RawExtension{Raw: []byte(
		`[{"name":"v1","emptyDir":{"sizeLimit":"2Gi"}}]`,
	)}

	got1 := ExtraVolumesDigest(base)
	got2 := ExtraVolumesDigest(changed)
	if got1 == "" || got2 == "" {
		t.Fatal("expected non-empty digests")
	}
	if got1 == got2 {
		t.Error("digest did not change when emptyDir.sizeLimit changed")
	}
}

func TestExtraVolumesDigest_CsiVolumeAttributesChangeIsDetected(t *testing.T) {
	base := &runtime.RawExtension{Raw: []byte(
		`[{"name":"v1","csi":{"driver":"example.csi","volumeAttributes":{"key":"a"}}}]`,
	)}
	changed := &runtime.RawExtension{Raw: []byte(
		`[{"name":"v1","csi":{"driver":"example.csi","volumeAttributes":{"key":"b"}}}]`,
	)}

	got1 := ExtraVolumesDigest(base)
	got2 := ExtraVolumesDigest(changed)
	if got1 == "" || got2 == "" {
		t.Fatal("expected non-empty digests")
	}
	if got1 == got2 {
		t.Error("digest did not change when csi.volumeAttributes changed")
	}
}

func TestNormalizeExtraVolumes_DoesNotAliasInput(t *testing.T) {
	input := &runtime.RawExtension{Raw: []byte(`[{"name":"v1","emptyDir":{}}]`)}
	inputCopy := append([]byte(nil), input.Raw...)

	got := NormalizeExtraVolumes(input)
	if got == nil {
		t.Fatal("expected a non-nil normalized result")
	}
	if &got.Raw[0] == &input.Raw[0] {
		t.Fatal("NormalizeExtraVolumes aliased the input's byte slice")
	}

	// Mutating the returned bytes must not affect the caller's original RawExtension.
	got.Raw[0] = 'X'
	if !strings.HasPrefix(string(input.Raw), string(inputCopy[:1])) {
		t.Fatal("mutating the normalized result mutated the caller's input")
	}
}

// collectVolumeAndMountNames walks pod for every volume/mount name the operator itself assigns:
// pod.Spec.Volumes, plus every VolumeMount on every container and init container. Container names
// (e.g. "copy-cli" the init container, "otel-packages-installer", "uio-loader-init") are
// deliberately excluded: extraVolumes can never collide with a container name, only with a
// volume/mount name, and ReservedVolumeNames exists to protect the latter.
func collectVolumeAndMountNames(pod *corev1.Pod) []string {
	var names []string
	for _, v := range pod.Spec.Volumes {
		names = append(names, v.Name)
	}
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			names = append(names, m.Name)
		}
	}
	for _, c := range pod.Spec.InitContainers {
		for _, m := range c.VolumeMounts {
			names = append(names, m.Name)
		}
	}
	return names
}

// collectMountPaths walks pod for every VolumeMount path the operator itself creates, across the
// main container and every init container.
func collectMountPaths(pod *corev1.Pod) []string {
	var paths []string
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			paths = append(paths, m.MountPath)
		}
	}
	for _, c := range pod.Spec.InitContainers {
		for _, m := range c.VolumeMounts {
			paths = append(paths, m.MountPath)
		}
	}
	return paths
}

// assertAllReserved fails with the required drift-guard message for any volume/mount name or
// mount path PodFactory.Create emitted that IsReservedVolumeName/IsReservedMountPath does not
// recognize. The mount-path check is what would have caught #2: the name-only check has always
// passed even when a mount path (e.g. /opt/weka_runtime.py) was left uncovered.
func assertAllReserved(t *testing.T, caseName string, pod *corev1.Pod) {
	t.Helper()
	for _, name := range collectVolumeAndMountNames(pod) {
		if !IsReservedVolumeName(name) {
			t.Errorf("%s: a new operator volume was added without reserving its name; user extraVolumes could now collide with it (volume/mount name %q)", caseName, name)
		}
	}
	for _, p := range collectMountPaths(pod) {
		if !IsReservedMountPath(p) {
			t.Errorf("%s: a new operator mount path was added without reserving it; user extraVolumeMounts could now collide with it (mount path %q)", caseName, p)
		}
	}
}

// TestOperatorVolumesAreAllReserved is the drift guard for ReservedVolumeNames: it builds real
// pods (via PodFactory.Create, not a hand-maintained approximation of it) across every mode/config
// combination known to add its own volumes or mounts, and asserts every resulting volume and
// mount name is covered by IsReservedVolumeName. A future change that adds an operator-managed
// volume without reserving its name breaks this test instead of silently leaving a name that
// extraVolumes could collide with.
//
// Cases and why each is here:
//   - compute: the baseline pod, no mode-specific volumes.
//   - smbw: adds smbw-shm (requires config.Config.Smbw.ShmSize, ConfigureEnv's usual source of it
//     does not run in a unit test).
//   - discovery: a distinct mode with its own base-volume wiring.
//   - drivers-loader, non-COS node: setDriverDependencies' non-COS branch (libmodules, usrsrc).
//   - drivers-loader, COS node: setDriverDependencies' COS branch (host-modules via
//     addUIOLoaderInitContainer, proc-sysrq-trigger, proc-cmdline).
//   - drivers-builder, COS node: COS branch plus IsDriversBuilder()'s extra gcloud-credentials.
//   - drivers-loader with a copy-weka-files-to-driver-loader instruction: copyWekaVersionToContainer
//     (shared-weka-version), reached unconditionally off IsDriversContainer() regardless of COS.
//   - ssdproxy: needsProxyMount's first disjunct (weka-proxy-socket-dir).
//   - drive with DriveCapacity>0 (UsesDriveSharing): needsProxyMount's second disjunct, same
//     volume name, different mode.
//   - adhoc-op with a sign-drives instruction carrying ssd_proxy_container_uuid:
//     getSsdUidForAdhocOp's consumer (weka-ssdproxy-local-socket).
//   - otel packages installer configured: the otel-packages-installer init container's volume
//     (otel-packages), gated by config.Config.Otel.PythonPackagesInstallerImage.
func TestOperatorVolumesAreAllReserved(t *testing.T) {
	t.Run("compute", func(t *testing.T) {
		pod, err := createTestPod(t, weka.WekaContainerModeCompute, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "compute", pod)
	})

	t.Run("smbw", func(t *testing.T) {
		originalShmSize := config.Config.Smbw.ShmSize
		config.Config.Smbw.ShmSize = "8Gi"
		defer func() { config.Config.Smbw.ShmSize = originalShmSize }()

		pod, err := createTestPod(t, weka.WekaContainerModeSmbw, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "smbw", pod)
	})

	t.Run("discovery", func(t *testing.T) {
		pod, err := createTestPod(t, weka.WekaContainerModeDiscovery, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "discovery", pod)
	})

	t.Run("drivers-loader non-COS", func(t *testing.T) {
		pod, err := createTestPodOnNode(t, weka.WekaContainerModeDriversLoader, &discovery.DiscoveryNodeInfo{}, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "drivers-loader non-COS", pod)
	})

	t.Run("drivers-loader COS", func(t *testing.T) {
		nodeInfo := &discovery.DiscoveryNodeInfo{Os: weka.OsNameCos}
		pod, err := createTestPodOnNode(t, weka.WekaContainerModeDriversLoader, nodeInfo, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "drivers-loader COS", pod)
	})

	t.Run("drivers-builder COS", func(t *testing.T) {
		nodeInfo := &discovery.DiscoveryNodeInfo{Os: weka.OsNameCos}
		pod, err := createTestPodOnNode(t, weka.WekaContainerModeDriversBuilder, nodeInfo, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "drivers-builder COS", pod)
	})

	t.Run("drivers-loader with copy-weka-files instruction", func(t *testing.T) {
		pod, err := createTestPod(t, weka.WekaContainerModeDriversLoader, func(spec *weka.WekaContainerSpec) {
			spec.Instructions = &weka.Instructions{
				Type:    weka.InstructionCopyWekaFilesToDriverLoader,
				Payload: `{"targetImage":"img:1","cliImage":"img:1"}`,
			}
		})
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "drivers-loader with copy-weka-files instruction", pod)
	})

	t.Run("ssdproxy", func(t *testing.T) {
		pod, err := createTestPod(t, weka.WekaContainerModeSSDProxy, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "ssdproxy", pod)
	})

	t.Run("drive with drive sharing", func(t *testing.T) {
		pod, err := createTestPod(t, weka.WekaContainerModeDrive, func(spec *weka.WekaContainerSpec) {
			spec.DriveCapacity = 100
		})
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "drive with drive sharing", pod)
	})

	t.Run("adhoc-op sign-drives with ssd proxy uuid", func(t *testing.T) {
		pod, err := createTestPod(t, weka.WekaContainerModeAdhocOp, func(spec *weka.WekaContainerSpec) {
			spec.Instructions = &weka.Instructions{
				Type:    weka.InstructionTypeSignDrives,
				Payload: `{"ssd_proxy_container_uuid":"11111111-1111-1111-1111-111111111111"}`,
			}
		})
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "adhoc-op sign-drives with ssd proxy uuid", pod)
	})

	t.Run("otel packages installer configured", func(t *testing.T) {
		original := config.Config.Otel.PythonPackagesInstallerImage
		config.Config.Otel.PythonPackagesInstallerImage = "otel-installer:latest"
		defer func() { config.Config.Otel.PythonPackagesInstallerImage = original }()

		pod, err := createTestPod(t, weka.WekaContainerModeCompute, nil)
		if err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		assertAllReserved(t, "otel packages installer configured", pod)
	})
}
