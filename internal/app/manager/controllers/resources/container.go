package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type WekaLocalPs struct {
	Name            string `json:"name"`
	RunStatus       string `json:"runStatus"`
	LastFailureText string `json:"lastFailureText"`
}

type WekaLocalStatusSlot struct {
	ClusterID string `json:"cluster_guid"`
}

type WekaLocalStatusContainer struct {
	Slots []WekaLocalStatusSlot `json:"slots"`
}

type WekaLocalContainerGetIdentityValue struct {
	ClusterId   string `json:"cluster_guid"`
	ContainerId int    `json:"host_id"`
}
type WekaLocalContainerGetIdentityResponse struct {
	Value *WekaLocalContainerGetIdentityValue `json:"value"`
}

type WekaLocalStatusResponse map[string]WekaLocalStatusContainer

type ContainerFactory struct {
	container *wekav1alpha1.WekaContainer
}

type WekaDriveResponse struct {
	HostId string `json:"host_id"`
}

func (driveResponse *WekaDriveResponse) ContainerId() (int, error) {
	return HostIdToContainerId(driveResponse.HostId)
}

func NewContainerFactory(container *wekav1alpha1.WekaContainer) *ContainerFactory {
	return &ContainerFactory{
		container: container,
	}
}

func (f *ContainerFactory) Create(ctx context.Context) (*corev1.Pod, error) {
	labels := labelsForWekaPod(f.container)

	image := f.container.Spec.Image

	imagePullSecrets := []corev1.LocalObjectReference{}
	if f.container.Spec.ImagePullSecret != "" {
		imagePullSecrets = []corev1.LocalObjectReference{
			{Name: f.container.Spec.ImagePullSecret},
		}
	}

	netDevice := "udp"
	udpMode := "false"
	if f.container.Spec.Network.EthDevice != "" {
		netDevice = f.container.Spec.Network.EthDevice
	}
	if len(f.container.Spec.Network.EthDevices) > 0 {
		netDevice = strings.Join(f.container.Spec.Network.EthDevices, ",")
	}
	if f.container.Spec.Network.UdpMode {
		netDevice = "udp"
		udpMode = "true"
	}

	var terminationGracePeriodSeconds int64 = 60 * 60 * 24 * 7
	//if f.container.Spec.Mode == "drive" {
	//	terminationGracePeriodSeconds = 60
	//}

	hostNetwork := true
	if f.container.IsHostNetwork() {
		hostNetwork = false
	}

	if f.container.Spec.TracesConfiguration == nil {
		f.container.Spec.TracesConfiguration = &wekav1alpha1.TracesConfiguration{
			MaxCapacityPerIoNode: 10,
			EnsureFreeSpace:      20,
		}
	}

	tolerations := f.getTolerations()

	debugSleep := os.Getenv("WEKA_OPERATOR_DEBUG_SLEEP")
	if debugSleep == "" {
		debugSleep = "3"
	}

	containerPathPersistence := "/opt/weka-persistence"
	hostsideContainerPersistence := fmt.Sprintf("%s/%s", f.container.GetHostsideContainerPersistence(), f.container.GetUID())
	hostsideClusterPersistence := fmt.Sprintf("%s/%s", f.container.GetHostsideClusterPersistence(), "cluster-less")
	if len(f.container.GetOwnerReferences()) > 0 {
		clusterId := f.container.GetOwnerReferences()[0].UID
		hostsideClusterPersistence = fmt.Sprintf("%s/%s", f.container.GetHostsideClusterPersistence(), clusterId)
	}
	wekaPort := strconv.Itoa(f.container.Spec.Port)

	serviceAccountName := f.container.Spec.ServiceAccountName
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.container.Name,
			Namespace: f.container.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Tolerations: tolerations,
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/os",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"linux"},
									},
								},
							},
						},
					},
				},
			},
			ImagePullSecrets:              imagePullSecrets,
			TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
			Containers: []corev1.Container{
				{
					Image:           image,
					Name:            "weka-container",
					ImagePullPolicy: corev1.PullAlways,
					Command:         []string{"python3", "/opt/weka_runtime.py"},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &[]bool{true}[0],
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "dev",
							MountPath: "/dev",
						},
						{
							Name:      "hugepages",
							MountPath: "/dev/hugepages",
						},
						{
							Name:      "sys",
							MountPath: "/sys",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/opt/weka_runtime.py",
							SubPath:   "weka_runtime.py",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/etc/syslog-ng/syslog-ng.conf",
							SubPath:   "syslog-ng.conf",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/usr/local/bin/wekaauthcli",
							SubPath:   "run-weka-cli.sh",
						},
						{
							Name:      "osrelease",
							MountPath: "/hostside/etc/os-release",
						},
					},
					Env: []corev1.EnvVar{
						{
							Name:  "AGENT_PORT",
							Value: strconv.Itoa(f.container.Spec.AgentPort),
						},
						{
							Name:  "NAME",
							Value: f.container.Spec.WekaContainerName,
						},
						{
							Name:  "MODE",
							Value: f.container.Spec.Mode,
						},
						{
							Name:  "PORT",
							Value: wekaPort,
						},
						{
							Name:  "MEMORY",
							Value: f.getHugePagesDetails().WekaMemoryString,
						},
						{
							Name:  "NETWORK_DEVICE",
							Value: netDevice,
						},
						{
							Name:  "UDP_MODE",
							Value: udpMode,
						},
						{
							Name:  "WEKA_PORT",
							Value: wekaPort,
						},
						{
							Name:  "WEKA_CLI_DEBUG",
							Value: "0",
						},
						{
							Name:  "DIST_SERVICE",
							Value: f.container.Spec.DriversDistService,
						},
						{
							Name:  "WEKA_PERSISTENCE_DIR",
							Value: containerPathPersistence,
						},
						{
							Name:  "MAX_TRACE_CAPACITY_GB",
							Value: strconv.Itoa(f.container.Spec.TracesConfiguration.MaxCapacityPerIoNode * (f.container.Spec.NumCores + 1)),
						},
						{
							Name:  "ENSURE_FREE_SPACE_GB",
							Value: strconv.Itoa(f.container.Spec.TracesConfiguration.EnsureFreeSpace),
						},
						{
							Name:  "IMAGE_NAME",
							Value: image,
						},
						{
							Name:  "WEKA_OPERATOR_DEBUG_SLEEP",
							Value: debugSleep,
						},
						{
							Name:  "OS_DISTRO",
							Value: f.container.Spec.OsDistro,
						},
						{
							Name: "OS_BUILD_ID",
							Value: func() string {
								if f.container.Spec.COSBuildSpec != nil {
									return f.container.Spec.COSBuildSpec.OsBuildId
								}
								return ""
							}(),
						},
					},
				},
			},
			HostNetwork: hostNetwork,
			Volumes: []corev1.Volume{
				{
					Name: "hugepages",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMedium(fmt.Sprintf("HugePages-%s", f.getHugePagesDetails().HugePagesK8sSuffix)),
						},
					},
				},
				{
					Name: "osrelease",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/etc/os-release",
							Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
						},
					},
				},
				{
					Name: "dev",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/dev",
						},
					},
				},
				{
					Name: "sys",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/sys",
						},
					},
				},
				{
					Name: "weka-boot-scripts",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "weka-boot-scripts",
							},
							DefaultMode: &[]int32{0o777}[0],
						},
					},
				},
			},
		},
	}

	if !f.container.IsDiscoveryContainer() {
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-container-persistence-dir",
			MountPath: containerPathPersistence,
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-container-persistence-dir",
			MountPath: "/var/log",
			SubPath:   "var/log",
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-container-persistence-dir",
			MountPath: f.container.GetHostsidePersistenceBaseLocation() + "/boot-level",
			SubPath:   "tmpfss/boot-level",
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-cluster-persistence-dir",
			MountPath: f.container.GetHostsidePersistenceBaseLocation() + "/node-cluster",
			SubPath:   "shared-configs",
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "weka-container-persistence-dir",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hostsideContainerPersistence,
					Type: &[]corev1.HostPathType{corev1.HostPathDirectoryOrCreate}[0],
				},
			},
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "weka-cluster-persistence-dir",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hostsideClusterPersistence,
					Type: &[]corev1.HostPathType{corev1.HostPathDirectoryOrCreate}[0],
				},
			},
		})

	}

	if f.container.IsDiscoveryContainer() && f.container.IsCos() {
		allowCosHugepageConfig := os.Getenv("COS_ALLOW_HUGEPAGE_CONFIG")
		if allowCosHugepageConfig != "true" {
			allowCosHugepageConfig = "false"
		}
		if allowCosHugepageConfig == "true" {
			globalCosHugepageSize := os.Getenv("COS_GLOBAL_HUGEPAGE_SIZE")
			if globalCosHugepageSize == "" {
				globalCosHugepageSize = "2m"
			}
			globalCosHugepageCount := os.Getenv("COS_GLOBAL_HUGEPAGE_COUNT")
			if globalCosHugepageCount == "" {
				return nil, errors.New("COS_GLOBAL_HUGEPAGE_COUNT env var is not set")
			}
			if _, err := strconv.Atoi(globalCosHugepageCount); err != nil {
				return nil, errors.New("COS_GLOBAL_HUGEPAGE_COUNT env var is not a number")
			}

			// for discovery containers, set COS params for hugepages
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "COS_ALLOW_HUGEPAGE_CONFIG",
				Value: allowCosHugepageConfig,
			})
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "COS_GLOBAL_HUGEPAGE_SIZE",
				Value: globalCosHugepageSize,
			})
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "COS_GLOBAL_HUGEPAGE_COUNT",
				Value: globalCosHugepageCount,
			})
		}
	}

	err := f.setResources(ctx, pod)
	if err != nil {
		return nil, err
	}

	if len(f.container.Spec.JoinIps) != 0 {
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "JOIN_IPS",
			Value: strings.Join(f.container.Spec.JoinIps, ","),
		})
	}

	matchExpression := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
	if f.container.Spec.NodeAffinity != "" {
		matchExpression = append(matchExpression, corev1.NodeSelectorRequirement{
			Key:      "kubernetes.io/hostname",
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{f.container.Spec.NodeAffinity},
		})
	}
	if f.container.Spec.NodeSelector != nil {
		for k, v := range f.container.Spec.NodeSelector {
			matchExpression = append(matchExpression, corev1.NodeSelectorRequirement{
				Key:      k,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{v},
			})
		}
	}

	if serviceAccountName != "" {
		pod.Spec.ServiceAccountName = serviceAccountName
	}

	pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions = matchExpression

	if f.container.Spec.NodeInfoConfigMap != "" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "node-info",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: f.container.Spec.NodeInfoConfigMap,
					},
				},
			},
		})

		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "node-info",
			MountPath: "/etc/wekaio/node-info",
		})
	}

	if f.container.Spec.WekaSecretRef.SecretKeyRef != nil && f.container.Spec.WekaSecretRef.SecretKeyRef.Key != "" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "weka-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: f.container.Spec.WekaSecretRef.SecretKeyRef.Key,
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-credentials",
			MountPath: "/var/run/secrets/weka-operator/operator-user",
		})
	}

	for name, secret := range f.container.Spec.AdditionalSecrets {
		volumeName := fmt.Sprintf("%s-secret", name)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secret,
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: fmt.Sprintf("/var/run/secrets/weka-operator/%s", name),
		})
	}

	if f.container.IsDriversContainer() {
		// adding mount of headers only for case of drivers-related container
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "libmodules",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/lib/modules",
				},
			},
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "usrsrc",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/usr/src",
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "libmodules",
			MountPath: "/lib/modules",
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "usrsrc",
			MountPath: "/usr/src",
		})
	}

	return pod, nil
}

func (f *ContainerFactory) getTolerations() []corev1.Toleration {
	tolerations := []corev1.Toleration{
		{
			Key:      "node.kubernetes.io/not-ready",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
		{
			Key:      "node.kubernetes.io/unreachable",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
		{
			Key:      "node.kubernetes.io/network-unavailable",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
		{
			Key:      "node.kubernetes.io/unschedulable",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
		{
			Key:      "node.kubernetes.io/disk-pressure",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
		{
			Key:      "node.kubernetes.io/disk-pressure",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
		{
			Key:      "node.kubernetes.io/cpu-pressure",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
		{
			Key:      "node.kubernetes.io/cpu-pressure",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
		{
			Key:      "node.kubernetes.io/memory-pressure",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
		{
			Key:      "node.kubernetes.io/memory-pressure",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
	}
	// expand with custom tolerations
	for _, t := range f.container.Spec.Tolerations {
		tolerations = append(tolerations, t)
	}
	return tolerations
}

type HugePagesDetails struct {
	HugePagesStr          string
	HugePagesK8sSuffix    string
	HugePagesMb           int
	WekaMemoryString      string
	HugePagesResourceName corev1.ResourceName
}

func (f *ContainerFactory) getHugePagesDetails() HugePagesDetails {
	hugePagesStr := ""
	hugePagesK8sSuffix := "2Mi"
	wekaMemoryString := ""
	if f.container.Spec.HugepagesSize == "1Gi" {
		hugePagesK8sSuffix = f.container.Spec.HugepagesSize
		hugePagesStr = fmt.Sprintf("%dGi", f.container.Spec.Hugepages/1000)
		wekaMemoryString = fmt.Sprintf("%dGiB", f.container.Spec.Hugepages/1000)
	} else {
		hugePagesStr = fmt.Sprintf("%dMi", f.container.Spec.Hugepages)
		hugePagesK8sSuffix = "2Mi"
		wekaMemoryString = fmt.Sprintf("%dMiB", f.container.Spec.Hugepages-200)
	}

	if f.container.Spec.HugepagesOverride != "" {
		wekaMemoryString = f.container.Spec.HugepagesOverride
	}

	hugePagesName := corev1.ResourceName(
		strings.Join(
			[]string{corev1.ResourceHugePagesPrefix, hugePagesK8sSuffix},
			""))

	return HugePagesDetails{
		HugePagesStr:          hugePagesStr,
		HugePagesK8sSuffix:    hugePagesK8sSuffix,
		WekaMemoryString:      wekaMemoryString,
		HugePagesResourceName: hugePagesName,
		HugePagesMb:           f.container.Spec.Hugepages,
	}
}

func (f *ContainerFactory) setResources(ctx context.Context, pod *corev1.Pod) error {
	totalNumCores := f.container.Spec.NumCores
	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
		totalNumCores += f.container.Spec.ExtraCores
	}

	_, logger, end := instrumentation.GetLogSpan(ctx, "setResources",
		"cores", totalNumCores,
		"mode", f.container.Spec.Mode,
		"cpuPolicy", f.container.Spec.CpuPolicy,
	)
	defer end()

	cpuPolicy := f.container.Spec.CpuPolicy
	if !cpuPolicy.IsValid() {
		return fmt.Errorf("invalid CPU policy: %s", cpuPolicy)
	}

	hgDetails := f.getHugePagesDetails()

	if cpuPolicy == wekav1alpha1.CpuPolicyAuto {
		if len(f.container.Spec.CoreIds) > 0 {
			cpuPolicy = wekav1alpha1.CpuPolicyManual
		}
	}

	if cpuPolicy == wekav1alpha1.CpuPolicyShared {
		cpuPolicy = wekav1alpha1.CpuPolicyManual // shared is a special case of manual, where topology/allocmap keeps track of affinities
	}

	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "CORES",
		Value: strconv.Itoa(f.container.Spec.NumCores),
	})

	var cpuRequestStr string
	var cpuLimitStr string

	switch cpuPolicy {
	case wekav1alpha1.CpuPolicyDedicatedHT:
		totalCores := totalNumCores*2 + 1
		if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
			totalCores = totalNumCores // inconsistency with pre-allocation, but we rather not allocate envoy too much too soon
		}
		if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			totalCores = totalCores - f.container.Spec.ExtraCores // basically reducing back what we over-allocated
			// i.e: both for envoy and 3, extraCores(envoy is hard coded to 1 now), means "ht if possible", otherwise full core
		}
		cpuRequestStr = fmt.Sprintf("%d", totalCores)
		cpuLimitStr = cpuRequestStr
	case wekav1alpha1.CpuPolicyDedicated:
		totalCores := totalNumCores + 1
		if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
			totalCores = totalNumCores // inconsistency with pre-allocation, but we rather not allocate envoy too much too soon
		}
		cpuRequestStr = fmt.Sprintf("%d", totalCores)
		cpuLimitStr = cpuRequestStr
	case wekav1alpha1.CpuPolicyManual:
		cpuRequestStr = fmt.Sprintf("%dm", 1000*totalNumCores+100)
		cpuLimitStr = fmt.Sprintf("%dm", 1000*(totalNumCores+1))
	}

	if cpuPolicy == wekav1alpha1.CpuPolicyDedicatedHT {
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "CORE_IDS",
			Value: "auto",
		})
	}

	if cpuPolicy == wekav1alpha1.CpuPolicyDedicated {
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "CORE_IDS",
			Value: "auto",
		})
	}

	if cpuPolicy == wekav1alpha1.CpuPolicyManual {
		if len(f.container.Spec.CoreIds) == 0 {
			return errors.New("CPU policy manual is not supported without coreIds")
		}
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "CORE_IDS",
			Value: comaSeparated(f.container.Spec.CoreIds),
		})
	}

	memRequest := "7000Mi"
	if f.container.IsDriversContainer() {
		memRequest = "3000M"
		cpuRequestStr = "500m"
		cpuLimitStr = "2000m"
	}
	if slices.Contains([]string{wekav1alpha1.WekaContainerModeDiscovery}, f.container.Spec.Mode) {
		memRequest = "500M"
		cpuRequestStr = "500m"
		cpuLimitStr = "500m"
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeClient {
		managementMemory := 1965
		perFrontendMemory := 2050
		buffer := 1150
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perFrontendMemory*totalNumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
		managementMemory := 3000
		perDriveMemory := 2100
		buffer := 1800
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perDriveMemory*totalNumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeCompute {
		managementMemory := 2200
		perComputeMemory := 3600
		buffer := 1600
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perComputeMemory*totalNumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
		s3Memory := 16000
		managementMemory := 1965 + s3Memory // 8000 per S3
		perFrontendMemory := 2050
		buffer := 450
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perFrontendMemory*f.container.Spec.NumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
		total := 1024
		memRequest = fmt.Sprintf("%dMi", total)
	}

	// since this is HT, we are doubling num of cores on allocation
	logger.SetValues("cpuRequestStr", cpuRequestStr, "cpuLimitStr", cpuLimitStr, "memRequest", memRequest, "hugePages", hgDetails.HugePagesStr)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse(cpuLimitStr),
			hgDetails.HugePagesResourceName: resource.MustParse(hgDetails.HugePagesStr),
			corev1.ResourceMemory:           resource.MustParse(memRequest),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse(cpuRequestStr),
			hgDetails.HugePagesResourceName: resource.MustParse(hgDetails.HugePagesStr),
			corev1.ResourceMemory:           resource.MustParse(memRequest),
			corev1.ResourceEphemeralStorage: resource.MustParse("8M"),
		},
	}

	return nil
}

func labelsForWekaPod(container *wekav1alpha1.WekaContainer) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":      "WekaContainer",
		"app.kubernetes.io/instance":  container.Name,
		"app.kubernetes.io/part-of":   "weka-operator",
		"app.kubernetes.io/create-by": "controller-manager",
	}
	for k, v := range container.ObjectMeta.Labels {
		labels[k] = v
	}
	return labels
}

func comaSeparated(ints []int) string {
	var result []string
	for _, i := range ints {
		result = append(result, strconv.Itoa(i))
	}
	return strings.Join(result, ",")
}

func GetContainerName(cluster *wekav1alpha1.WekaCluster, role string, i int) string {
	return fmt.Sprintf("%s-%s-%d", cluster.Name, role, i)
}
