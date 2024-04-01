package resources

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WekaLocalPs struct {
	RunStatus       string `json:"runStatus"`
	LastFailureText string `json:"lastFailureText"`
}

type WekaLocalStatusSlot struct {
	ClusterID string `json:"cluster_guid"`
}

type WekaLocalStatusContainer struct {
	Slots []WekaLocalStatusSlot `json:"slots"`
}

type WekaLocalStatusResponse map[string]WekaLocalStatusContainer

type ContainerFactory struct {
	container *wekav1alpha1.WekaContainer
	logger    logr.Logger
}

type WekaDriveResponse struct {
	HostId string `json:"host_id"`
}

type WekaUsersResponse struct {
	//OrgId    int    `json:"org_id"`
	//PosixGid string `json:"posix_gid"`
	//PosixUid string `json:"posix_uid"`
	//Role     string `json:"role"`
	//S3Policy string `json:"s3_policy"`
	//Source   string `json:"source"`
	//Uid      string `json:"uid"`
	Username string `json:"username"`
}

func (driveResponse *WekaDriveResponse) ContainerId() (int, error) {
	return HostIdToContainerId(driveResponse.HostId)
}

func NewContainerFactory(container *wekav1alpha1.WekaContainer, logger logr.Logger) *ContainerFactory {
	return &ContainerFactory{
		container: container,
		logger:    logger.WithName("ContainerFactory"),
	}
}

func (f *ContainerFactory) Create() (*corev1.Pod, error) {
	labels := labelsForWekaContainer(f.container)

	image := f.container.Spec.Image

	//TODO: to resolve basing on value from spec
	//hugePagesNum := f.container.Spec.Hugepages

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

	resourceLimit := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(fmt.Sprintf("%dm", 1000*(f.container.Spec.NumCores+1))),
		hugePagesName:      resource.MustParse(hugePagesStr),
	}
	resourceRequest := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(fmt.Sprintf("%dm", 1000*(f.container.Spec.NumCores))),
		hugePagesName:      resource.MustParse(hugePagesStr),
	}

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
	if f.container.Spec.Network.UdpMode {
		netDevice = "udp"
		udpMode = "true"
	}

	var terminationGracePeriodSeconds int64 = 10
	if f.container.Spec.Mode == "drive" {
		terminationGracePeriodSeconds = 60
	}

	hostNetwork := true
	if f.container.Spec.Mode == "dist" || f.container.Spec.Mode == "drivers-loader" {
		hostNetwork = false
	}

	wekaPersistenceDir := "/opt/weka-persistence"
	persistentPathBase := fmt.Sprintf("/opt/k8s-weka/containers/%s", f.container.GetUID())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.container.Name,
			Namespace: f.container.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/hostname",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{f.container.Spec.NodeAffinity},
									},
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
							MountPath: "/opt/start-weka-agent.sh",
							SubPath:   "start-weka-agent.sh",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/opt/start-weka-container.sh",
							SubPath:   "start-weka-container.sh",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/etc/supervisord/supervisord.conf",
							SubPath:   "supervisord.conf",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/etc/syslog-ng/syslog-ng.conf",
							SubPath:   "syslog-ng.conf",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/opt/start-syslog-ng.sh",
							SubPath:   "start-syslog-ng.sh",
						},
						{
							Name:      "weka-boot-scripts",
							MountPath: "/usr/local/bin/wekaauthcli",
							SubPath:   "run-weka-cli.sh",
						},
						{
							Name:      "weka-container-persistence-dir",
							MountPath: wekaPersistenceDir,
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
							Value: strconv.Itoa(f.container.Spec.Port),
						},
						{
							Name:  "MEMORY",
							Value: wekaMemoryString,
						},
						{
							Name:  "CORES",
							Value: strconv.Itoa(f.container.Spec.NumCores),
						},
						{
							Name:  "CORE_IDS",
							Value: comaSeparated(f.container.Spec.CoreIds),
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
							Value: strconv.Itoa(f.container.Spec.Port),
						},
						{
							Name:  "WEKA_CLI_DEBUG",
							Value: "0",
						},
						{
							Name:  "DIST_SERVICE",
							Value: f.container.Spec.DriversDistService,
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits:   resourceLimit,
						Requests: resourceRequest,
					},
				},
			},
			HostNetwork: hostNetwork,
			Volumes: []corev1.Volume{
				{
					Name: "hugepages",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMedium(fmt.Sprintf("HugePages-%s", hugePagesK8sSuffix)),
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
				{
					Name: "weka-container-persistence-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: persistentPathBase,
							Type: &[]corev1.HostPathType{corev1.HostPathDirectoryOrCreate}[0],
						},
					},
				},
			},
		},
	}

	if f.container.Spec.WekaSecretRef.SecretKeyRef != nil {
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

	if f.container.Spec.Mode == "dist" || f.container.Spec.Mode == "drivers-loader" {
		// adding mount of headers only for case of dist service container
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "libmodules",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/lib/modules",
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "libmodules",
			MountPath: "/lib/modules",
		})
	}

	return pod, nil
}

func labelsForWekaContainer(container *wekav1alpha1.WekaContainer) map[string]string {
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
