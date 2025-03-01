package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/go-weka-observability/instrumentation"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/discovery"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const inPodHostBinds = "/host-binds"

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
	Value     *WekaLocalContainerGetIdentityValue `json:"value"`
	HasValue  bool                                `json:"has_value"`
	Exception *string                             `json:"exception,omitempty"`
}

type WekaLocalStatusResponse map[string]WekaLocalStatusContainer

type PodFactory struct {
	container *wekav1alpha1.WekaContainer
	nodeInfo  *discovery.DiscoveryNodeInfo
}

type WekaDriveResponse struct {
	HostId string `json:"host_id"`
}

type ShutdownInstructions struct {
	AllowStop      bool `json:"allow_stop"`
	AllowForceStop bool `json:"allow_force_stop"`
}

const globalPersistenceMountPath = "/opt/weka-global-persistence"

func (driveResponse *WekaDriveResponse) ContainerId() (int, error) {
	return HostIdToContainerId(driveResponse.HostId)
}

func NewPodFactory(container *wekav1alpha1.WekaContainer, nodeInfo *discovery.DiscoveryNodeInfo) *PodFactory {
	return &PodFactory{
		nodeInfo:  nodeInfo,
		container: container,
	}
}

func (f *PodFactory) Create(ctx context.Context, podImage *string) (*corev1.Pod, error) {
	labels := LabelsForWekaPod(f.container)

	image := f.container.Spec.Image
	// if podImage is not nil, use it instead of the image from the container spec
	// NOTE: used for the cases when it's not allowed to upgrade weka image
	if podImage != nil {
		image = *podImage
	}

	if image == "" {
		return nil, errors.New("image is required")
	}

	imagePullSecrets := []corev1.LocalObjectReference{}
	if f.container.Spec.ImagePullSecret != "" {
		imagePullSecrets = []corev1.LocalObjectReference{
			{Name: f.container.Spec.ImagePullSecret},
		}
	}

	netDevice := "udp"
	udpMode := "false"
	subnets := strings.Join(f.container.Spec.Network.DeviceSubnets, ",")
	// if subnet for devices auto-discovery is set, we don't need to set the netDevice
	if subnets != "" {
		netDevice = ""
	}
	gateway := f.container.Spec.Network.Gateway
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

	var terminationGracePeriodSeconds int64 = 60 * 60 * 24 * 365 // 1 year
	//if f.container.Spec.Mode == "drive" {
	//	terminationGracePeriodSeconds = 60
	//}

	hostNetwork := false
	if f.container.IsHostNetwork() {
		hostNetwork = true
	}

	if f.container.Spec.TracesConfiguration == nil {
		f.container.Spec.TracesConfiguration = &wekav1alpha1.TracesConfiguration{
			MaxCapacityPerIoNode: 10,
			EnsureFreeSpace:      20,
		}
	}

	tolerations := f.getTolerations()

	debugSleep := config.Config.DebugSleep
	if f.container.Spec.GetOverrides().DebugSleepOnTerminate > 0 {
		debugSleep = f.container.Spec.GetOverrides().DebugSleepOnTerminate
	}

	hostsideContainerPersistence := f.nodeInfo.GetContainerPersistencePath(f.container.GetUID())
	hostsideSharedData := f.nodeInfo.GetContainerSharedDataPath(f.container.GetUID())
	hostsideClusterPersistence := fmt.Sprintf("%s/%s", f.nodeInfo.GetHostsideClusterPersistence(), "cluster-less")
	if len(f.container.GetOwnerReferences()) > 0 {
		clusterId := f.container.GetOwnerReferences()[0].UID
		hostsideClusterPersistence = fmt.Sprintf("%s/%s", f.nodeInfo.GetHostsideClusterPersistence(), clusterId)
	}
	wekaPort := strconv.Itoa(f.container.GetPort())

	serviceAccountName := f.container.Spec.ServiceAccountName
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.container.Name,
			Namespace: f.container.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Tolerations:                   tolerations,
			TopologySpreadConstraints:     f.container.Spec.TopologySpreadConstraints,
			Affinity:                      f.initAffinities(ctx, f.container.Spec.Affinity),
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
					Ports: func() []corev1.ContainerPort {
						if len(f.container.Spec.ExposePorts) == 0 {
							return nil
						}
						ports := make([]corev1.ContainerPort, len(f.container.Spec.ExposePorts))
						for i, port := range f.container.Spec.ExposePorts {
							ports[i] = corev1.ContainerPort{
								ContainerPort: int32(port),
								Protocol:      corev1.ProtocolTCP,
							}
						}
						return ports
					}(),
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "dev",
							MountPath: "/dev",
						},
						{
							Name:      "run",
							MountPath: "/host/run",
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
							MountPath: "/usr/local/bin/weka",
							SubPath:   "run-weka-cli.sh",
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
							Value: strconv.Itoa(f.container.GetAgentPort()),
						},
						{
							Name:  "WEKA_CONTAINER_ID",
							Value: string(f.container.GetUID()),
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
							Name:  "SUBNETS",
							Value: subnets,
						},
						{
							Name:  "NET_GATEWAY",
							Value: gateway,
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
							Value: strconv.Itoa(debugSleep),
						},
						// use Downward API to get the name of the node where the Pod is executing
						{
							Name: "NODE_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
						{
							Name: "POD_ID",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.uid",
								},
							},
						},
						{
							Name:  "AUTO_REMOVE_TIMEOUT",
							Value: strconv.Itoa(int(f.container.Spec.AutoRemoveTimeout.Seconds())),
						},
					},
				},
			},
			HostNetwork: hostNetwork,
			HostPID:     f.container.Spec.HostPID,
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
					Name: "run",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/run",
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

	if f.container.Spec.GetOverrides().PreRunScript != "" {
		// encode in base64 and write into env var
		base64str := base64.StdEncoding.EncodeToString([]byte(f.container.Spec.GetOverrides().PreRunScript))
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "PRE_RUN_SCRIPT",
			Value: base64str,
		})
	}

	if f.container.Spec.PortRange != nil {
		// vars needed for clients to dinamically set ports
		envVars := []corev1.EnvVar{
			{
				Name:  "BASE_PORT",
				Value: strconv.Itoa(f.container.Spec.PortRange.BasePort),
			},
			{
				Name:  "PORT_RANGE",
				Value: strconv.Itoa(f.container.Spec.PortRange.PortRange),
			},
		}
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, envVars...)
	}

	if f.container.HasPersistentStorage() {
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-container-persistence-dir",
			MountPath: inPodHostBinds + "/opt-weka",
		})

		// TODO: For now we are keeping ephmeral /opt/k8s-weka data along with global

		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-container-persistence-dir",
			MountPath: "/var/log",
			SubPath:   "var/log",
		})

		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name: "weka-container-persistence-dir",
			// TODO: Should not use gethostsidepersistent
			MountPath: inPodHostBinds + "/boot-level",
			SubPath:   "tmpfss/boot-level",
		})

		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name: "weka-container-shared-dir", // shared between node-agent and pods
			// TODO: Should not use gethostsidepersistent
			MountPath: inPodHostBinds + "/shared",
		})

		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-cluster-persistence-dir",
			MountPath: inPodHostBinds + "/shared-configs",
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
			Name: "weka-container-shared-dir",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hostsideSharedData,
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

		if config.Config.LocalDataPvc != "" {
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "weka-container-global-persistence-dir",
				MountPath: globalPersistenceMountPath,
			})
			// Global PVC for all weka nodes
			pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
				Name: "weka-container-global-persistence-dir",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: config.Config.LocalDataPvc,
					},
				},
			})
			// lets inform runtime via env var on expected behaviour
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "WEKA_PERSISTENCE_MODE",
				Value: "global",
			})
		}
	}

	if f.container.IsDiscoveryContainer() {
		allowCosHugepageConfig := config.Config.GkeCompatibility.HugepageConfiguration.Enabled
		if allowCosHugepageConfig {
			globalCosHugepageSize := config.Config.GkeCompatibility.HugepageConfiguration.Size
			globalCosHugepageCount := config.Config.GkeCompatibility.HugepageConfiguration.Count
			// for discovery containers, set COS params for hugepages
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "WEKA_COS_ALLOW_HUGEPAGE_CONFIG",
				Value: "true",
			})
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "WEKA_COS_GLOBAL_HUGEPAGE_SIZE",
				Value: globalCosHugepageSize,
			})
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "WEKA_COS_GLOBAL_HUGEPAGE_COUNT",
				Value: strconv.Itoa(globalCosHugepageCount),
			})
		}
	}

	if f.container.IsAdhocOpContainer() {
		instructionsBytes, err := json.Marshal(f.container.Spec.Instructions)
		if err != nil {
			return nil, err
		}

		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "INSTRUCTIONS",
			Value: string(instructionsBytes),
		})
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

	if serviceAccountName != "" {
		pod.Spec.ServiceAccountName = serviceAccountName
	}

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

	// DiscoveryContainer on GKE only will force boot to set up hugepages. This is managed via Helm configuration
	if f.container.IsDiscoveryContainer() {
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "proc-sysrq-trigger",
			MountPath: "/hostside/proc/sysrq-trigger",
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "proc-cmdline",
			MountPath: "/hostside/proc/cmdline",
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "proc-sysrq-trigger",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/proc/sysrq-trigger",
					Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
				},
			},
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "proc-cmdline",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/proc/cmdline",
					Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
				},
			},
		})
	}

	if f.container.IsDriversContainer() { // Dependencies for driver-loader probably can be reduced
		if f.nodeInfo.IsCos() {
			allowCosDisableDriverSigning := config.Config.GkeCompatibility.DisableDriverSigning
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "weka-boot-scripts",
				MountPath: "/devenv.sh",
				SubPath:   "devenv.sh",
			})
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "proc-sysrq-trigger",
				MountPath: "/hostside/proc/sysrq-trigger",
			})
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "proc-cmdline",
				MountPath: "/hostside/proc/cmdline",
			})
			pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
				Name: "proc-sysrq-trigger",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/proc/sysrq-trigger",
						Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
					},
				},
			})
			pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
				Name: "proc-cmdline",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/proc/cmdline",
						Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
					},
				},
			})
			if allowCosDisableDriverSigning {
				pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
					Name:  "WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING",
					Value: "true",
				})
			}

			if f.container.IsDriversBuilder() {
				pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
					Name: "gcloud-credentials",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: config.Config.GkeCompatibility.ServiceAccountSecret,
						},
					},
				})
				pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
					Name:      "gcloud-credentials",
					MountPath: "/var/secrets/google",
				})
				pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
					Name:  "GOOGLE_APPLICATION_CREDENTIALS",
					Value: "/var/secrets/google/service-account.json",
				})
			}
		} else {
			libModulesPath := "/lib/modules"
			usrSrcPath := "/usr/src"
			if f.nodeInfo.IsRhCos() {
				libModulesPath = "/hostpath/lib/modules"
				usrSrcPath = "/hostpath/usr/src"
			}

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
				MountPath: libModulesPath,
			})
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "usrsrc",
				MountPath: usrSrcPath,
			})
		}
	}

	// for Dist container, if running on OCP / COS / other containerized OS
	if f.container.IsDriversBuilder() && f.nodeInfo.IsRhCos() {
		if f.nodeInfo.InitContainerImage == "" {
			return nil, errors.New("cannot create build container for OCP without Toolkit image")
		}
		ocpPullSecret := config.Config.OcpCompatibility.DriverToolkitSecretName
		if ocpPullSecret != "" {
			// we need to add the buildkit image pull secret
			pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{
				Name: ocpPullSecret,
			})
		}
		// add ephemeral volume to share the buildkit container
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "driver-toolkit-shared", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})

		ContainerMountProp := corev1.MountPropagationHostToContainer
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "driver-toolkit-shared", MountPath: "/driver-toolkit-shared", ReadOnly: false, MountPropagation: &ContainerMountProp})

		initContainerCmds := make(map[string][]string)
		initContainerCmds[wekav1alpha1.OsNameOpenshift] = []string{"/bin/sh", "-c",
			"mkdir -p /driver-toolkit-shared/lib; " +
				"mkdir -p /driver-toolkit-shared/usr; " +
				"cp -RLrf /usr/src /driver-toolkit-shared/usr; " +
				"cp -RLrf /lib/modules /driver-toolkit-shared/lib"}

		cmd := initContainerCmds[wekav1alpha1.OsNameOpenshift]
		mountProp := corev1.MountPropagationBidirectional
		buildkitContainer := corev1.Container{
			Name:          "driver-toolkit-init",
			Image:         f.nodeInfo.InitContainerImage,
			Command:       cmd,
			Env:           pod.Spec.Containers[0].Env,
			Resources:     corev1.ResourceRequirements{},
			ResizePolicy:  nil,
			RestartPolicy: nil,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "driver-toolkit-shared", MountPath: "/driver-toolkit-shared", ReadOnly: false, MountPropagation: &mountProp},
			},
			SecurityContext: pod.Spec.Containers[0].SecurityContext,
		}
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, buildkitContainer)
	}

	err = f.setAffinities(ctx, pod)
	if err != nil {
		return nil, err
	}

	return pod, nil
}

func (f *PodFactory) getTolerations() []corev1.Toleration {
	tolerations := []corev1.Toleration{
		{
			Key:      "weka.io/shutdown-node",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
	}

	if !config.Config.SkipUnhealthyToleration {
		unhealthyTolerations := []corev1.Toleration{
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
		}
		tolerations = append(tolerations, unhealthyTolerations...)
	}

	pressureTolerations := []corev1.Toleration{
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
	tolerations = append(tolerations, pressureTolerations...)
	// for client we are ignoring PreferNoSchedule, the rest should not ignore(for now)
	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeClient {
		if !config.Config.SkipClientPreferNoScheduleToleration {
			preferNoScheduleTolerations := []corev1.Toleration{
				{
					// operator must be Exists when `key` is empty, which means "match all values and all keys"
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectPreferNoSchedule,
				},
			}
			tolerations = append(tolerations, preferNoScheduleTolerations...)
		}
	}

	// expand with custom tolerations
	tolerations = append(tolerations, f.container.Spec.Tolerations...)
	return tolerations
}

type HugePagesDetails struct {
	HugePagesStr          string
	HugePagesK8sSuffix    string
	HugePagesMb           int
	WekaMemoryString      string
	HugePagesResourceName corev1.ResourceName
}

func (f *PodFactory) getHugePagesDetails() HugePagesDetails {
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
		offset := f.getHugePagesOffset()
		wekaMemoryString = fmt.Sprintf("%dMiB", f.container.Spec.Hugepages-offset)
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

func (f *PodFactory) getHugePagesOffset() int {
	offset := f.container.Spec.HugepagesOffset
	// get default if not set
	if offset == 0 {
		if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
			offset = 200 * f.container.Spec.NumDrives
		} else {
			offset = 200
		}
	}
	return offset
}

func (f *PodFactory) setResources(ctx context.Context, pod *corev1.Pod) error {
	totalNumCores := f.container.Spec.NumCores
	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
		totalNumCores += f.container.Spec.ExtraCores
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeNfs {
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
		if f.nodeInfo.IsHt {
			cpuPolicy = wekav1alpha1.CpuPolicyDedicatedHT
		} else {
			cpuPolicy = wekav1alpha1.CpuPolicyDedicated
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
		if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeNfs {
			totalCores = totalCores - f.container.Spec.ExtraCores
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
			Value: commaSeparated(f.container.Spec.CoreIds),
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
		perFrontendMemory := 3050
		buffer := 2000
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perFrontendMemory*totalNumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
		managementMemory := 4000
		perDriveProcessBuffer := 800
		perDriveProcessMemory := 2200 + perDriveProcessBuffer
		perDriveMemory := 700
		buffer := 4000
		total := buffer +
			managementMemory +
			perDriveProcessMemory*totalNumCores +
			perDriveMemory*f.container.Spec.NumDrives +
			f.container.Spec.AdditionalMemory
		memRequest = fmt.Sprintf("%dMi", total)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeCompute {
		managementMemory := 2700
		perComputeBuffer := 800
		perComputeMemory := 4400 + perComputeBuffer
		buffer := 4000
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perComputeMemory*totalNumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
		s3Memory := 16000
		managementMemory := 1965 + s3Memory // 8000 per S3
		perFrontendMemory := 2050
		buffer := 450
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perFrontendMemory*f.container.Spec.NumCores+f.container.Spec.AdditionalMemory)
	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeNfs {
		nfsMemory := 16000
		managementMemory := 1965 + nfsMemory // 8000 per NFS
		perFrontendMemory := 2050
		buffer := 450
		memRequest = fmt.Sprintf("%dMi", buffer+managementMemory+perFrontendMemory*f.container.Spec.NumCores+f.container.Spec.AdditionalMemory)

	}

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
		total := 1024 + f.container.Spec.AdditionalMemory
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

	if f.container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
		pod.Spec.Containers[0].Resources.Requests["weka.io/drives"] = resource.MustParse(strconv.Itoa(f.container.Spec.NumDrives))
		pod.Spec.Containers[0].Resources.Limits["weka.io/drives"] = resource.MustParse(strconv.Itoa(f.container.Spec.NumDrives))
	}

	return nil
}

func (f *PodFactory) initAffinities(ctx context.Context, affinity *corev1.Affinity) *corev1.Affinity {
	var result *corev1.Affinity
	if affinity != nil {
		result = affinity.DeepCopy()
	} else {
		result = &corev1.Affinity{}
	}

	osRequired := corev1.NodeSelectorRequirement{
		Key:      "kubernetes.io/os",
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{"linux"},
	}

	if result.NodeAffinity == nil {
		result.NodeAffinity = &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							osRequired,
						},
					},
				},
			},
		}
	} else {
		matchExpression := result.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
		matchExpression = append(matchExpression, osRequired)
		result.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions = matchExpression
	}

	return result
}

func (f *PodFactory) setAffinities(ctx context.Context, pod *corev1.Pod) error {
	//TODO: Set high priority class for node-bound containers
	matchExpression := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
	if f.container.GetNodeAffinity() != "" {
		matchExpression = append(matchExpression, corev1.NodeSelectorRequirement{
			Key:      "kubernetes.io/hostname",
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{string(f.container.GetNodeAffinity())},
		})
	}

	if len(f.container.Spec.NodeSelector) != 0 {
		for k, v := range f.container.Spec.NodeSelector {
			matchExpression = append(matchExpression, corev1.NodeSelectorRequirement{
				Key:      k,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{v},
			})
		}
	}

	pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions = matchExpression

	clusterId := f.container.GetParentClusterId()
	if f.container.IsAllocatable() && !f.container.Spec.NoAffinityConstraints {
		term := corev1.PodAffinityTerm{
			LabelSelector: &metav1.LabelSelector{},
			TopologyKey:   "kubernetes.io/hostname",
		}
		if !f.container.IsProtocolContainer() {
			// S3: we dont want to allow more then one s3 container per node
			// Other types of containers we validate to be once for cluster
			term.LabelSelector.MatchLabels = client.MatchingLabels{
				domain.WekaLabelClusterId: clusterId,
				domain.WekaLabelMode:      f.container.Spec.Mode,
			}
		} else {
			term.LabelSelector.MatchExpressions = []metav1.LabelSelectorRequirement{
				{
					Key:      domain.WekaLabelMode,
					Operator: metav1.LabelSelectorOpIn,
					Values:   domain.ContainerModesWithFrontend,
				},
			}
		}

		// generalize above code using mode
		if pod.Spec.Affinity.PodAntiAffinity == nil {
			pod.Spec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{term},
			}
		} else {
			terms := pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			terms = append(terms, term)
			pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = terms
		}

		if f.container.IsEnvoy() {
			// schedule together with s3, required during scheduling
			term := corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"weka.io/mode":       wekav1alpha1.WekaContainerModeS3,
						"weka.io/cluster-id": clusterId,
					},
				},
				TopologyKey: "kubernetes.io/hostname",
			}
			if pod.Spec.Affinity.PodAffinity == nil {
				pod.Spec.Affinity.PodAffinity = &corev1.PodAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{term},
				}
			} else {
				terms := pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
				terms = append(terms, term)
				pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution = terms
			}
		}
	}

	if f.container.Spec.NoAffinityConstraints {
		// TODO: Keeping this as a reference, once there is a fix for 7-containers detection, might try using FD again
		// preserving save machine identifier. Weka still might justifiably not allow to put different FDs on the same node
		// but then it will be cleaner ask, to have a manual override that ignores specifically that

		//// set failure domain to ID of wekaContainer
		//pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
		//	Name:  "FAILURE_DOMAIN",
		//	Value: util.GetLastGuidPart(f.container.GetUID()),
		//})
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "MACHINE_IDENTIFIER",
			Value: string(f.container.GetUID()),
		})
	}

	return nil
}

func LabelsForWekaPod(container *wekav1alpha1.WekaContainer) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":      "WekaContainer",
		"app.kubernetes.io/part-of":   "weka-operator",
		"app.kubernetes.io/create-by": "controller-manager",
		"weka.io/mode":                container.Spec.Mode,
	}
	for k, v := range container.ObjectMeta.Labels {
		labels[k] = v
	}

	if container.GetParentClusterId() != "" {
		labels["weka.io/cluster-id"] = container.GetParentClusterId()
	}
	return labels
}

func commaSeparated(ints []int) string {
	var result []string
	for _, i := range ints {
		result = append(result, strconv.Itoa(i))
	}
	return strings.Join(result, ",")
}

func GetPodShutdownInstructionPathOnAgent(bootId string, pod *corev1.Pod) string {
	containerUid := pod.ObjectMeta.OwnerReferences[0].UID
	podUid := pod.UID
	return path.Join("/host-binds/shared/containers/", string(containerUid), "instructions", string(podUid), bootId)
}
