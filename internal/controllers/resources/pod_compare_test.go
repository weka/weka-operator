package resources

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

type samplePodCase struct {
	name   string
	mode   string
	cfg    func()
	mutate func(*weka.WekaContainerSpec)
}

// samplePodCases switch on every optional backend feature pod.go has.
func samplePodCases(t *testing.T) []samplePodCase {
	t.Helper()
	extraVolumes := rawVolumes(t, []corev1.Volume{
		{Name: "corp-ca", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	})
	return []samplePodCase{
		{name: "compute", mode: weka.WekaContainerModeCompute},
		{
			name: "compute with every option",
			mode: weka.WekaContainerModeCompute,
			cfg: func() {
				config.Config.NetnsEnabled = true
				config.Config.Otel.PythonPackagesInstallerImage = "otel-installer:latest"
				config.Config.PriorityClasses.Initial = "weka-initial"
			},
			mutate: func(spec *weka.WekaContainerSpec) {
				spec.CpuPolicy = weka.CpuPolicyManual
				spec.CoreIds = []int{1, 2}
				spec.NonDatapathCoreIds = []int{3}
				spec.PVC = &weka.PVCConfig{Name: "weka-pvc"}
				spec.WekaSecretRef.SecretKeyRef = &corev1.SecretKeySelector{Key: "weka-creds"}
				spec.AdditionalSecrets = map[string]string{"wekahome-cacert": "ca-secret"}
				spec.Network.NvidiaVfSingleIp = ptr.To(true)
				spec.Network.AllocateVfPerIoNode = ptr.To(true)
				spec.Numa = &weka.WekaNuma{Single: true, Region: ptr.To(1)}
				spec.NoAffinityConstraints = true
				spec.NodeInfoConfigMap = "node-info-cm"
				spec.HostPID = true
				spec.ImagePullSecret = "pull-secret"
				spec.JoinIps = []string{"10.0.0.1"}
				spec.ExposePorts = []int{8080}
				spec.PortRange = &weka.PortRange{BasePort: 45000, PortRange: 100}
				spec.Ipv6 = true
				spec.Overrides = &weka.WekaContainerSpecOverrides{PreRunScript: "true", NoReserveSpace: true, DebugSleepOnTerminate: 5}
				spec.ExtraVolumes = extraVolumes
				spec.ExtraVolumeMounts = []corev1.VolumeMount{{Name: "corp-ca", MountPath: "/opt/corp-ca"}}
			},
		},
		{
			name: "compute numa dra",
			mode: weka.WekaContainerModeCompute,
			mutate: func(spec *weka.WekaContainerSpec) {
				spec.Numa = &weka.WekaNuma{Single: true, Region: ptr.To(0), Method: weka.WekaNumaMethodDra}
			},
		},
		{
			name: "compute 1Gi hugepages",
			mode: weka.WekaContainerModeCompute,
			mutate: func(spec *weka.WekaContainerSpec) {
				spec.HugepagesSize = "1Gi"
			},
		},
		{
			name:   "drive",
			mode:   weka.WekaContainerModeDrive,
			mutate: func(spec *weka.WekaContainerSpec) { spec.NumDrives = 2 },
		},
		{
			name: "drive with drive sharing",
			mode: weka.WekaContainerModeDrive,
			mutate: func(spec *weka.WekaContainerSpec) {
				spec.NumDrives = 2
				spec.DriveCapacity = 100
			},
		},
		{
			name: "drive with container capacity",
			mode: weka.WekaContainerModeDrive,
			mutate: func(spec *weka.WekaContainerSpec) {
				spec.ContainerCapacity = 500
				spec.DriveTypesRatio = &weka.DriveTypesRatio{Tlc: 1, Qlc: 1}
			},
		},
		{name: "ssdproxy", mode: weka.WekaContainerModeSSDProxy},
		{
			name: "smbw",
			mode: weka.WekaContainerModeSmbw,
			cfg:  func() { config.Config.Smbw.ShmSize = "8Gi" },
		},
	}
}

func buildSamplePods(t *testing.T) map[string]*corev1.Pod {
	t.Helper()
	saved := config.Config
	t.Cleanup(func() { config.Config = saved })
	t.Setenv("WEKA_POD_SECURITY_CONTEXT", `{"fsGroup":1000}`)

	pods := map[string]*corev1.Pod{}
	for _, tc := range samplePodCases(t) {
		config.Config = saved
		if tc.cfg != nil {
			tc.cfg()
		}
		pods[tc.name] = mustPod(t, tc.mode, tc.mutate)
	}
	config.Config = saved
	return pods
}

func setEnv(c *corev1.Container, name, value string) {
	for i := range c.Env {
		if c.Env[i].Name == name {
			c.Env[i].Value = value
			c.Env[i].ValueFrom = nil
			return
		}
	}
	c.Env = append(c.Env, corev1.EnvVar{Name: name, Value: value})
}

func dropEnv(c *corev1.Container, name string) {
	c.Env = slices.DeleteFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == name })
}

func dropVolume(pod *corev1.Pod, name string) {
	pod.Spec.Volumes = slices.DeleteFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == name })
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = slices.DeleteFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == name })
}

func mustPod(t *testing.T, mode string, mutate func(*weka.WekaContainerSpec)) *corev1.Pod {
	t.Helper()
	pod, err := createTestPod(t, mode, mutate)
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return pod
}

func assertNoDiff(t *testing.T, diff []string) {
	t.Helper()
	if len(diff) != 0 {
		t.Errorf("unexpected diff: %q", diff)
	}
}

// assertDiff requires exactly len(want) entries, each starting with one of want.
func assertDiff(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("diff = %q, want %d entries starting with %q", got, len(want), want)
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, w) }) {
			t.Errorf("diff %q has no entry starting with %q", got, w)
		}
	}
}

func reformatQuantity(name corev1.ResourceName, q resource.Quantity) resource.Quantity {
	if name == corev1.ResourceCPU {
		return resource.MustParse(fmt.Sprintf("%dm", q.MilliValue()))
	}
	return resource.MustParse(strconv.FormatInt(q.Value(), 10))
}

// addAPIServerNoise applies what the API server, admission and the environment add to a live pod without the
// operator asking for it.
func addAPIServerNoise(pod *corev1.Pod) {
	token := corev1.VolumeMount{Name: "kube-api-access-xyz", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true}
	pod.Spec.Volumes = append(pod.Spec.Volumes,
		corev1.Volume{Name: "kube-api-access-xyz", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{}}},
		corev1.Volume{Name: "live-extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	)
	pod.Spec.ResourceClaims = append(pod.Spec.ResourceClaims, corev1.PodResourceClaim{Name: "injected-claim", ResourceClaimName: ptr.To("injected")})
	pod.Spec.TerminationGracePeriodSeconds = ptr.To[int64](30)
	pod.Spec.SecurityContext = &corev1.PodSecurityContext{}
	pod.Labels = map[string]string{"injected": "label"}
	pod.Annotations = map[string]string{"injected": "annotation"}

	for i := range pod.Spec.Volumes {
		src := &pod.Spec.Volumes[i].VolumeSource
		if src.HostPath != nil && src.HostPath.Type == nil {
			src.HostPath.Type = ptr.To(corev1.HostPathUnset)
		}
		if src.Secret != nil && src.Secret.DefaultMode == nil {
			src.Secret.DefaultMode = ptr.To(corev1.SecretVolumeSourceDefaultMode)
		}
		if src.ConfigMap != nil && src.ConfigMap.DefaultMode == nil {
			src.ConfigMap.DefaultMode = ptr.To(corev1.ConfigMapVolumeSourceDefaultMode)
		}
	}
	if pod.Spec.DNSPolicy == "" {
		pod.Spec.DNSPolicy = corev1.DNSClusterFirst
	}
	if pod.Spec.ServiceAccountName == "" {
		pod.Spec.ServiceAccountName = "default"
	}
	pod.Spec.DeprecatedServiceAccount = pod.Spec.ServiceAccountName

	apply := func(c *corev1.Container) {
		c.ImagePullPolicy = corev1.PullAlways
		c.TerminationMessagePath = "/dev/termination-log"
		c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		c.VolumeMounts = append(c.VolumeMounts, token)
		for _, list := range []corev1.ResourceList{c.Resources.Requests, c.Resources.Limits} {
			for name, q := range list {
				list[name] = reformatQuantity(name, q)
			}
		}
		c.Resources.Limits[corev1.ResourceEphemeralStorage] = resource.MustParse("1Gi")
		for name, q := range c.Resources.Limits {
			if _, ok := c.Resources.Requests[name]; !ok {
				c.Resources.Requests[name] = q
			}
		}
	}

	for i := range pod.Spec.InitContainers {
		ic := &pod.Spec.InitContainers[i]
		apply(ic)
		ic.Resources.Requests[corev1.ResourceEphemeralStorage] = resource.MustParse("2Gi")
	}

	c := &pod.Spec.Containers[0]
	apply(c)
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "live-extra", MountPath: "/opt/live-extra"})
	c.Resources.Claims = append(c.Resources.Claims, corev1.ResourceClaim{Name: "injected-claim"})
	c.Env = append(c.Env, corev1.EnvVar{Name: "GOMAXPROCS", Value: "8"})
	c.SecurityContext.AllowPrivilegeEscalation = ptr.To(true)
	for i := range c.Env {
		if c.Env[i].ValueFrom != nil && c.Env[i].ValueFrom.FieldRef != nil {
			c.Env[i].ValueFrom.FieldRef.APIVersion = "v1"
		}
	}
}

func TestDiffPodsRebuiltPodHasNoDiff(t *testing.T) {
	first, second := buildSamplePods(t), buildSamplePods(t)
	for name, pod := range first {
		t.Run(name, func(t *testing.T) {
			assertNoDiff(t, DiffPods(pod, second[name], []string{"wekahome-cacert"}, false))
		})
	}
}

func TestDiffPodsIgnoresAPIServerNoise(t *testing.T) {
	for name, expected := range buildSamplePods(t) {
		t.Run(name, func(t *testing.T) {
			live := expected.DeepCopy()
			addAPIServerNoise(live)
			assertNoDiff(t, DiffPods(live, expected, []string{"wekahome-cacert"}, false))
		})
	}
}

func TestDiffPodsIgnoresExcludedValues(t *testing.T) {
	expected := buildSamplePods(t)
	for name, pod := range expected {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			removed := pod.DeepCopy()
			for envName := range excludedEnvNames {
				setEnv(&changed.Spec.Containers[0], envName, "other-value")
				dropEnv(&removed.Spec.Containers[0], envName)
			}
			for _, live := range []*corev1.Pod{changed, removed} {
				c := &live.Spec.Containers[0]
				c.Image = "other-image:2"
				for i := range live.Spec.InitContainers {
					live.Spec.InitContainers[i].Image = "other-installer:1@sha256:0123456789abcdef"
				}
				for resourceName := range excludedResourceNames {
					c.Resources.Requests[resourceName] = resource.MustParse("999")
					c.Resources.Limits[resourceName] = resource.MustParse("999")
				}
				live.Spec.HostPID = !live.Spec.HostPID
				live.Spec.PriorityClassName = "other-priority"
				live.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "other-pull-secret"}}
				dropVolume(live, "node-info")
				live.Spec.Volumes = append(live.Spec.Volumes, corev1.Volume{
					Name:         "node-info",
					VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "other-cm"}}},
				})
				c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "node-info", MountPath: "/etc/wekaio/node-info"})
				assertNoDiff(t, DiffPods(live, pod, []string{"wekahome-cacert"}, false))
			}
		})
	}
}

func TestDiffPodsReportsChanges(t *testing.T) {
	t.Run("numCores", func(t *testing.T) {
		live := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) { s.NumCores = 2 })
		expected := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) { s.NumCores = 4 })
		assertDiff(t, DiffPods(live, expected, nil, false),
			"CORES 2→4", "MAX_TRACE_CAPACITY_GB 30→50",
			"requests.cpu ", "limits.cpu ", "requests.memory ", "limits.memory ")
	})

	t.Run("nvidiaVfSingleIp only on live", func(t *testing.T) {
		live := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) { s.Network.NvidiaVfSingleIp = ptr.To(true) })
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		assertDiff(t, DiffPods(live, expected, nil, false), "NVIDIA_VF_SINGLE_IP true→<none>")
	})

	t.Run("nvidiaVfSingleIp only on expected", func(t *testing.T) {
		live := mustPod(t, weka.WekaContainerModeCompute, nil)
		expected := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) { s.Network.NvidiaVfSingleIp = ptr.To(false) })
		assertDiff(t, DiffPods(live, expected, nil, false), "NVIDIA_VF_SINGLE_IP <none>→false")
	})

	t.Run("numa region", func(t *testing.T) {
		withRegion := func(region int) *corev1.Pod {
			return mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) {
				s.Numa = &weka.WekaNuma{Single: true, Region: ptr.To(region)}
			})
		}
		assertDiff(t, DiffPods(withRegion(0), withRegion(1), nil, false),
			"requests.weka.io/numa-region-0 1→<none>", "requests.weka.io/numa-region-1 <none>→1",
			"limits.weka.io/numa-region-0 1→<none>", "limits.weka.io/numa-region-1 <none>→1")
	})

	t.Run("numa claim", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) {
			s.Numa = &weka.WekaNuma{Single: true, Region: ptr.To(0), Method: weka.WekaNumaMethodDra}
		})
		live := expected.DeepCopy()
		live.Spec.ResourceClaims = nil
		live.Spec.Containers[0].Resources.Claims = nil
		assertDiff(t, DiffPods(live, expected, nil, false), "claim numa <none>→present", "resourceClaim numa <none>→present")

		live = expected.DeepCopy()
		live.Spec.ResourceClaims[0].ResourceClaimName = ptr.To("other-claim")
		assertDiff(t, DiffPods(live, expected, nil, false), "resourceClaim numa changed")
	})

	t.Run("otel init container removed from live", func(t *testing.T) {
		saved := config.Config
		defer func() { config.Config = saved }()
		config.Config.Otel.PythonPackagesInstallerImage = "otel-installer:latest"
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		live.Spec.InitContainers = nil
		assertDiff(t, DiffPods(live, expected, nil, false), "initContainer otel-packages-installer <none>→present")

		live = expected.DeepCopy()
		live.Spec.InitContainers[0].Image = "otel-installer:latest@sha256:0123456789abcdef"
		assertNoDiff(t, DiffPods(live, expected, nil, false))

		live = expected.DeepCopy()
		live.Spec.InitContainers[0].Command = []string{"sh", "-c", "true"}
		assertDiff(t, DiffPods(live, expected, nil, false), "initContainer otel-packages-installer changed")
	})

	t.Run("wekahome cacert volume removed from live", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) {
			s.AdditionalSecrets = map[string]string{"wekahome-cacert": "ca-secret"}
		})
		live := expected.DeepCopy()
		dropVolume(live, "wekahome-cacert-secret")
		assertDiff(t, DiffPods(live, expected, nil, false),
			"volume wekahome-cacert-secret <none>→present",
			"mount /var/run/secrets/weka-operator/wekahome-cacert <none>→wekahome-cacert-secret")
	})

	t.Run("additional secret volume is compared only for a listed key", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, func(s *weka.WekaContainerSpec) {
			s.AdditionalSecrets = map[string]string{"extra": "extra-secret"}
		})
		live := expected.DeepCopy()
		for i := range live.Spec.Volumes {
			if live.Spec.Volumes[i].Name == "extra-secret" {
				live.Spec.Volumes[i].Secret.SecretName = "other-secret"
			}
		}
		assertNoDiff(t, DiffPods(live, expected, nil, false))
		assertDiff(t, DiffPods(live, expected, []string{"extra"}, false), "volume extra-secret changed")
	})

	t.Run("volume source", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		for i := range live.Spec.Volumes {
			if live.Spec.Volumes[i].Name == "dev" {
				live.Spec.Volumes[i].HostPath.Path = "/other"
			}
		}
		assertDiff(t, DiffPods(live, expected, nil, false), "volume dev changed")
	})

	t.Run("mount", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		live.Spec.Containers[0].VolumeMounts = slices.DeleteFunc(live.Spec.Containers[0].VolumeMounts,
			func(m corev1.VolumeMount) bool { return m.MountPath == "/dev" })
		assertDiff(t, DiffPods(live, expected, nil, false), "mount /dev <none>→dev")
	})

	t.Run("dnsPolicy", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		live.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
		assertDiff(t, DiffPods(live, expected, nil, false), "spec.dnsPolicy ClusterFirstWithHostNet→ClusterFirst")
	})

	t.Run("single fields", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		live.Spec.HostNetwork = !expected.Spec.HostNetwork
		live.Spec.ServiceAccountName = "other"
		live.Spec.Containers[0].Command = []string{"other"}
		live.Spec.Containers[0].SecurityContext.Privileged = nil
		assertDiff(t, DiffPods(live, expected, nil, false),
			"spec.hostNetwork", "spec.serviceAccountName other→default",
			"containers[0].command", "containers[0].securityContext.privileged false→true")
	})

	t.Run("fieldRef path", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		for i := range live.Spec.Containers[0].Env {
			if live.Spec.Containers[0].Env[i].Name == "NODE_NAME" {
				live.Spec.Containers[0].Env[i].ValueFrom.FieldRef.FieldPath = "status.hostIP"
			}
		}
		assertDiff(t, DiffPods(live, expected, nil, false), "NODE_NAME fieldRef:status.hostIP→fieldRef:spec.nodeName")
	})

	t.Run("weka container is found by name", func(t *testing.T) {
		expected := mustPod(t, weka.WekaContainerModeCompute, nil)
		live := expected.DeepCopy()
		live.Spec.Containers = append([]corev1.Container{{Name: "sidecar", Image: "sidecar:1"}}, live.Spec.Containers...)
		assertNoDiff(t, DiffPods(live, expected, nil, false))
	})
}

func TestDiffPodsSkipCPU(t *testing.T) {
	expected := mustPod(t, weka.WekaContainerModeCompute, nil)

	cpuChanged := expected.DeepCopy()
	c := &cpuChanged.Spec.Containers[0]
	c.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("9")
	c.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("9")
	assertNoDiff(t, DiffPods(cpuChanged, expected, nil, true))
	assertDiff(t, DiffPods(cpuChanged, expected, nil, false), "requests.cpu 9→", "limits.cpu 9→")

	memoryChanged := expected.DeepCopy()
	memoryChanged.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("1Gi")
	assertDiff(t, DiffPods(memoryChanged, expected, nil, true), "requests.memory 1Gi→")
}

func TestDiffPodsConfigHashAnnotation(t *testing.T) {
	annotated := func(hash string) *corev1.Pod {
		pod := mustPod(t, weka.WekaContainerModeCompute, nil)
		pod.Annotations = map[string]string{consts.PodConfigHashAnnotation: hash}
		return pod
	}
	bare := mustPod(t, weka.WekaContainerModeCompute, nil)
	bare.Annotations = nil

	assertNoDiff(t, DiffPods(bare, annotated("a"), nil, false))
	assertNoDiff(t, DiffPods(annotated("a"), bare, nil, false))
	assertNoDiff(t, DiffPods(annotated("a"), annotated("a"), nil, false))
	assertDiff(t, DiffPods(annotated("a"), annotated("b"), nil, false), "podConfigHash a→b")
}

// excludedEnvNames classifies the env names pod.go writes on backend pods that DiffPods deliberately ignores.
var excludedEnvNames = map[string]string{
	"IMAGE_NAME":           "image has its own flow",
	"OTEL_SERVICE_VERSION": "operator version; would rotate every pod on every upgrade",
	"JOIN_IPS":             "expected pod gets the cluster's current join IPs; a running container is already connected",
	"PORT":                 "0 on the first pod, allocated later",
	"WEKA_PORT":            "0 on the first pod, allocated later",
	"AGENT_PORT":           "0 on the first pod, allocated later",
	"DIST_SERVICE":         "backend pod never reads it; the drivers-loader gets the spec value",
	"DRIVERS_BUILD_ID":     "backend pod never reads it; the drivers-loader gets the spec value",
	"BASE_PORT":            "client only",
	"PORT_RANGE":           "client only",
	"AUTO_REMOVE_TIMEOUT":  "set from the WekaClient only; always 0 on backends",
	"PRE_RUN_SCRIPT":       "changes only by a direct WekaContainer edit",
}

// notBackendEnvNames classifies the env names pod.go writes to pods that are never backend pods.
var notBackendEnvNames = map[string]string{
	"HTTPS_PROXY":                           "drivers-loader only",
	"INSTRUCTIONS":                          "ad-hoc operation only",
	"WEKA_COS_ALLOW_HUGEPAGE_CONFIG":        "discovery only",
	"WEKA_COS_GLOBAL_HUGEPAGE_SIZE":         "discovery only",
	"WEKA_COS_GLOBAL_HUGEPAGE_COUNT":        "discovery only",
	"WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING": "drivers containers on COS only",
	"GOOGLE_APPLICATION_CREDENTIALS":        "drivers builder on COS only",
	"TARGET_IMAGE_NAME":                     "drivers containers with a copy-weka-files instruction only",
}

func TestPodEnvNamesAreClassified(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	envName := regexp.MustCompile(`Name:\s+"([A-Z][A-Z0-9_]*)"`)
	written := map[string]struct{}{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range envName.FindAllStringSubmatch(string(src), -1) {
			written[m[1]] = struct{}{}
		}
	}

	classes := map[string][]string{
		"comparedEnvNames":   slices.Collect(maps.Keys(comparedEnvNames)),
		"excludedEnvNames":   slices.Collect(maps.Keys(excludedEnvNames)),
		"notBackendEnvNames": slices.Collect(maps.Keys(notBackendEnvNames)),
	}
	classesOf := map[string][]string{}
	for class, names := range classes {
		for _, name := range names {
			classesOf[name] = append(classesOf[name], class)
			if _, ok := written[name]; !ok {
				msg := "%s lists %s, which resources/*.go no longer writes — remove it"
				if class == "comparedEnvNames" {
					msg += " and bump consts.PodConfigCodeVersion"
				}
				t.Errorf(msg, class, name)
			}
		}
	}
	for name := range written {
		switch in := classesOf[name]; {
		case len(in) == 0:
			t.Errorf("resources/*.go writes env %s — add it to comparedEnvNames, excludedEnvNames or notBackendEnvNames", name)
		case len(in) > 1:
			t.Errorf("env %s is in more than one of comparedEnvNames, excludedEnvNames, notBackendEnvNames: %v", name, in)
		}
	}
}

// excludedResourceNames classifies the resource names pod.go can write on a backend pod that DiffPods deliberately ignores.
var excludedResourceNames = map[corev1.ResourceName]string{
	consts.ResourceSharedDrivesCapacity:                         "grown live by the capacity planner",
	consts.ResourcesSharedDrivesCapacityQLC:                     "grown live by the capacity planner",
	corev1.ResourceName(corev1.ResourceHugePagesPrefix + "1Gi"): "every backend template sets 2Mi; 1Gi only by a direct WekaContainer edit",
}

func assertSingleContainer(t *testing.T, podName string, pod *corev1.Pod) {
	t.Helper()
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("%s: pod.go builds %d containers — classify the new container and add a compare rule for it in DiffPods", podName, len(pod.Spec.Containers))
	}
}

func TestPodResourceAndInitNamesAreClassified(t *testing.T) {
	for podName, pod := range buildSamplePods(t) {
		assertSingleContainer(t, podName, pod)
		c := pod.Spec.Containers[0]
		for _, side := range []struct {
			side    string
			request bool
			list    corev1.ResourceList
		}{{"requests", true, c.Resources.Requests}, {"limits", false, c.Resources.Limits}} {
			for name := range side.list {
				if _, excluded := excludedResourceNames[name]; !comparedResource(name, side.request, false) && !excluded {
					t.Errorf("%s: pod.go sets %s.%s — add it to comparedResource or excludedResourceNames", podName, side.side, name)
				}
			}
		}
		for _, claim := range pod.Spec.ResourceClaims {
			if claim.Name != consts.WekaNumaClaimName {
				t.Errorf("%s: pod.go adds resource claim %q — extend DiffPods to compare it", podName, claim.Name)
			}
		}
		for _, ic := range pod.Spec.InitContainers {
			if ic.Name != otelInstallerContainerName {
				t.Errorf("%s: pod.go adds init container %q — extend DiffPods to compare it", podName, ic.Name)
			}
			for _, list := range []corev1.ResourceList{ic.Resources.Requests, ic.Resources.Limits} {
				for name := range list {
					if !slices.Contains(comparedInitResourceNames, name) {
						t.Errorf("%s: pod.go sets resource %s on init container %q — add it to comparedInitResourceNames or document why it is ignored", podName, name, ic.Name)
					}
				}
			}
		}
	}
}

// excludedFields classifies the other pod spec, weka container and init container fields pod.go sets that DiffPods
// deliberately ignores.
var excludedFields = map[string]string{
	"spec.affinity":                      "scheduling",
	"spec.tolerations":                   "scheduling; updated on the live pod in place",
	"spec.topologySpreadConstraints":     "scheduling",
	"spec.priorityClassName":             "differs between the first and later builds (Targeted class once status.nodeAffinity is set)",
	"spec.imagePullSecrets":              "changes via HandleSpecUpdates, matters only at image pull time",
	"spec.securityContext":               "environment-dependent: SCC and webhooks add fields",
	"spec.hostPID":                       "changes only by a direct WekaContainer edit",
	"spec.terminationGracePeriodSeconds": "admission policies rewrite it on every create",
	"containers[0].name":                 "lookup key",
	"containers[0].image":                "image has its own flow",
	"containers[0].imagePullPolicy":      "admission rewrites it on every create",
	"containers[0].ports":                "set from spec.exposePorts, which no controller writes (legacy field)",
	"initContainers[*].imagePullPolicy":  "admission rewrites it on every create",
	"initContainers[*].image":            "admission can pin it to a digest on every create, which would loop rotation; changes via helm, picked up at the next pod restart",
}

// keyedPaths are the pod and container fields DiffPods compares as keyed lists rather than through comparedFields.
var keyedPaths = []string{
	"spec.volumes", "spec.resourceClaims", "spec.containers", "spec.initContainers",
	"containers[0].env", "containers[0].volumeMounts", "containers[0].resources",
	"initContainers[*].name", "initContainers[*].command",
	"initContainers[*].resources", "initContainers[*].volumeMounts",
}

// unclassifiedPaths walks the non-zero fields of the pod spec, the weka container and each init container and
// returns those that DiffPods neither compares nor excludes.
func unclassifiedPaths(pod *corev1.Pod) []string {
	classified := map[string]struct{}{}
	for _, f := range comparedFields {
		classified[f.path] = struct{}{}
	}
	for path := range excludedFields {
		classified[path] = struct{}{}
	}
	for _, path := range keyedPaths {
		classified[path] = struct{}{}
	}

	var paths []string
	var walk func(v reflect.Value, prefix string)
	walk = func(v reflect.Value, prefix string) {
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}
			value := v.Field(i)
			path := prefix + name
			if value.IsZero() {
				continue
			}
			if _, ok := classified[path]; ok {
				continue
			}
			if value.Kind() == reflect.Pointer {
				value = value.Elem()
			}
			if value.Kind() == reflect.Struct {
				walk(value, path+".")
				continue
			}
			paths = append(paths, path)
		}
	}

	walk(reflect.ValueOf(pod.Spec), "spec.")
	walk(reflect.ValueOf(pod.Spec.Containers[0]), "containers[0].")
	for _, ic := range pod.Spec.InitContainers {
		walk(reflect.ValueOf(ic), "initContainers[*].")
	}
	sort.Strings(paths)
	return slices.Compact(paths)
}

func TestPodFieldsAreClassified(t *testing.T) {
	for name, pod := range buildSamplePods(t) {
		assertSingleContainer(t, name, pod)
		for _, path := range unclassifiedPaths(pod) {
			t.Errorf("%s: pod.go sets %s — add it to comparedFields or excludedFields", name, path)
		}
	}

	t.Run("reports an unclassified field", func(t *testing.T) {
		pod := mustPod(t, weka.WekaContainerModeCompute, nil)
		pod.Spec.Hostname = "host"
		pod.Spec.Containers[0].WorkingDir = "/work"
		pod.Spec.Containers[0].SecurityContext.RunAsUser = ptr.To[int64](1)
		got := unclassifiedPaths(pod)
		want := []string{"containers[0].securityContext.runAsUser", "containers[0].workingDir", "spec.hostname"}
		if !slices.Equal(got, want) {
			t.Errorf("unclassifiedPaths = %q, want %q", got, want)
		}
	})
}
