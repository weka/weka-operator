package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MaintainTraceSession struct {
	cluster          *weka.WekaCluster
	wekaHomeEndpoint string
	payload          *weka.RemoteTracesSessionConfig
	mgr              ctrl.Manager
	ownerRef         client.Object
	containerDetails weka.WekaOwnerDetails
	deployment       *apps.Deployment
	containers       []*weka.WekaContainer
	restClient       rest.Interface
	runningCallback  lifecycle.StepFunc
	successCallback  lifecycle.StepFunc
	failureCallback  lifecycle.StepFunc
	isExpired        bool
}

func NewMaintainTraceSession(
	mgr ctrl.Manager,
	restClient rest.Interface,
	payload *weka.RemoteTracesSessionConfig,
	ownerRef client.Object,
	containerDetails weka.WekaOwnerDetails,
	runningCallback lifecycle.StepFunc,
	successCallback lifecycle.StepFunc,
	failureCallback lifecycle.StepFunc,
	isExpired bool,
) *MaintainTraceSession {
	return &MaintainTraceSession{
		payload:          payload,
		mgr:              mgr,
		restClient:       restClient,
		ownerRef:         ownerRef,
		containerDetails: containerDetails,
		runningCallback:  runningCallback,
		successCallback:  successCallback,
		failureCallback:  failureCallback,
		isExpired:        isExpired,
	}
}

func (o *MaintainTraceSession) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "MaintainTraceSession",
		Run:  AsRunFunc(o),
	}
}

func (o *MaintainTraceSession) GetSteps() []lifecycle.Step {
	if o.isExpired {
		return []lifecycle.Step{
			&lifecycle.SimpleStep{Name: "CleanupResources", Run: o.CleanupResources},
		}
	}
	steps := []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "FetchCluster", Run: o.FetchCluster},
		&lifecycle.SimpleStep{Name: "DeduceWekaHomeUrl", Run: o.DeduceWekaHomeUrl},
		&lifecycle.SimpleStep{Name: "EnsureSecret", Run: o.EnsureSecret},
		&lifecycle.SimpleStep{Name: "EnsureWekaNodeRoutingConfigMap", Run: o.EnsureWekaNodeRoutingConfigMap},
		&lifecycle.SimpleStep{Name: "EnsureK8sContainerRoutingConfigMap", Run: o.EnsureK8sContainerRoutingConfigMap},
		&lifecycle.SimpleStep{Name: "EnsureDeployment", Run: o.EnsureDeployment},
		&lifecycle.SimpleStep{
			Name:       "UpdateStatusToRunning",
			Run:        o.runningCallback,
			Predicates: lifecycle.Predicates{func() bool { return o.runningCallback != nil }},
		},
		&lifecycle.SimpleStep{Name: "WaitTillExpiration", Run: o.WaitTillExpiration},
		&lifecycle.SimpleStep{
			Name:       "SuccessUpdate",
			Run:        o.successCallback,
			Predicates: lifecycle.Predicates{func() bool { return o.successCallback != nil }},
		},
	}

	return steps
}

type TraceSessionResult struct {
	Deployment string
	Cluster    string
	InProgress bool
}

func (o *MaintainTraceSession) GetJsonResult() string {
	result := TraceSessionResult{}

	if o.deployment != nil {
		result.Deployment = o.deployment.Name
	}
	if o.cluster != nil {
		result.Cluster = o.cluster.Name
	}

	// Check if expired
	startTime := o.ownerRef.GetCreationTimestamp()
	expirationTime := startTime.Add(o.payload.Duration.Duration)
	if meta.Now().After(expirationTime) {
		result.InProgress = false
	} else {
		result.InProgress = true
	}

	resultJson, _ := json.Marshal(result)
	return string(resultJson)
}

func (o *MaintainTraceSession) FetchCluster(ctx context.Context) error {
	c := o.mgr.GetClient()
	cluster := &weka.WekaCluster{}
	err := c.Get(ctx, client.ObjectKey{Name: o.payload.Cluster.Name, Namespace: o.payload.Cluster.Namespace}, cluster)
	if err != nil {
		return err
	}
	o.cluster = cluster
	return nil
}

func (o *MaintainTraceSession) DeduceWekaHomeUrl(ctx context.Context) error {
	wekaHomeEndpoint := config.Config.WekaHome.Endpoint
	defer func() {
		o.wekaHomeEndpoint = wekaHomeEndpoint
	}()

	if o.payload.WekahomeEndpointOverride != "" {
		wekaHomeEndpoint = o.payload.WekahomeEndpointOverride
		return nil
	}

	wekaService, err := o.getWekaService(ctx)
	if err == nil {
		status, err := wekaService.GetWekaStatus(ctx)
		if err == nil {
			if status.Cloud.Url != "" {
				wekaHomeEndpoint = status.Cloud.Url
				return nil
			}
		}
	}

	if o.cluster.Spec.WekaHome != nil {
		if o.cluster.Spec.WekaHome.Endpoint != "" {
			wekaHomeEndpoint = o.cluster.Spec.WekaHome.Endpoint
			return nil
		}
	}
	return nil
}

func (o *MaintainTraceSession) DeduceTaskmonUrl(wekaHomeUrl string) string {
	url := strings.TrimPrefix(wekaHomeUrl, "http://")
	url = strings.TrimPrefix(url, "https://")

	if strings.HasPrefix(url, "api.") {
		url = "taskmon." + strings.TrimPrefix(url, "api.")
	} else {
		url = "taskmon." + url
	}

	return url
}

func (o *MaintainTraceSession) DeduceTaskmonImage() string {
	if o.containerDetails.Image != "" {
		return o.containerDetails.Image
	}
	return config.Config.TaskmonDefaultImage
}

func (o *MaintainTraceSession) DeduceHostNetworkSettings() (bool, v1.DNSPolicy) {
	hostNetwork := false
	dnsPolicy := v1.DNSPolicy(config.Config.DNSPolicy.K8sNetwork)
	if o.payload.HostNetwork {
		hostNetwork = true
		dnsPolicy = v1.DNSPolicy(config.Config.DNSPolicy.HostNetwork)
	}
	return hostNetwork, dnsPolicy
}

func (o *MaintainTraceSession) EnsureDeployment(ctx context.Context) error {
	// create deployment object and apply it
	// generate token and store it in environment variable of pod
	labels := o.ownerRef.GetLabels()
	if len(labels) == 0 {
		labels = map[string]string{}
	}
	labels["app"] = "weka-trace-session"
	labels["weka.io/cluster-id"] = string(o.cluster.GetUID())

	annotations := o.ownerRef.GetAnnotations()
	if len(annotations) == 0 {
		annotations = map[string]string{}
	}

	hostNetwork, dnsPolicy := o.DeduceHostNetworkSettings()
	deployment := apps.Deployment{
		ObjectMeta: ctrl.ObjectMeta{
			Name:        "trace-session-" + o.ownerRef.GetName(),
			Namespace:   o.ownerRef.GetNamespace(),
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: apps.DeploymentSpec{
			Replicas: util.Int32Ref(1),
			Selector: &meta.LabelSelector{
				MatchLabels: map[string]string{
					"app":                "weka-trace-session",
					"weka.io/cluster-id": string(o.cluster.GetUID()),
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: ctrl.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: v1.PodSpec{
					ImagePullSecrets: []v1.LocalObjectReference{
						{Name: o.containerDetails.ImagePullSecret},
					},
					Tolerations:  o.containerDetails.Tolerations,
					NodeSelector: o.payload.NodeSelector,
					HostNetwork:  hostNetwork,
					DNSPolicy:    dnsPolicy,
					Containers: []v1.Container{
						{
							Name:    "weka-trace-session",
							Image:   o.DeduceTaskmonImage(),
							Command: []string{"/entrypoint.sh"},
							Env: []v1.EnvVar{
								{
									Name:  "TASKMON_TRACE_STREAMER_CONFIGS_SOURCE",
									Value: "hardcoded",
								},
								{
									Name:  "TASKMON_TRACE_STREAMER_CONFIGS_HARDCODED_CONFIGS_ADDRESS",
									Value: o.DeduceTaskmonUrl(o.wekaHomeEndpoint),
								},
								{
									Name:  "TASKMON_WEKA_HOME_TRACE_STREAMER_VERIFY_TLS",
									Value: util.BoolToShellString(!o.payload.AllowInsecureWekahomeEndpoint),
								},
								{
									Name:  "TASKMON_WEKA_HOME_TRACE_STREAMER_TLS",
									Value: util.BoolToShellString(!o.payload.AllowHttpWekahomeEndpoint),
								},
								{
									Name:  "TASKMON_TRACE_SERVER_VERIFY_TLS",
									Value: "false",
								},
								{
									Name:  "TASKMON_TRACE_SERVER_TLS",
									Value: "true",
								},
								{
									Name:  "TASKMON_SESSION_TOKEN_LOADER_TYPE",
									Value: "file",
								},
								{
									Name:  "TASKMON_SESSION_TOKEN_LOADER_PATH",
									Value: "/var/run/secrets/weka-operator/trace-session/token",
								},
								{
									Name:  "TASKMON_NODE_AND_CONTAINER_SERVER_DISCOVERY_TYPE",
									Value: "file",
								},
								{
									Name:  "TASKMON_NODE_AND_CONTAINER_SERVER_DISCOVERY_PATH",
									Value: "/weka-runtime/weka-trace-routing/trace-routing.json",
								},
								{
									Name:  "TASKMON_K8S_SERVER_DISCOVERY_TYPE",
									Value: "file",
								},
								{
									Name:  "TASKMON_K8S_SERVER_DISCOVERY_PATH",
									Value: "/weka-runtime/k8s-trace-routing/k8s-routing.json",
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "token",
									MountPath: "/var/run/secrets/weka-operator/trace-session",
								},
								{
									Name:      "weka-trace-routing",
									MountPath: "/weka-runtime/weka-trace-routing",
								},
								{
									Name:      "k8s-trace-routing",
									MountPath: "/weka-runtime/k8s-trace-routing",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "token",
							VolumeSource: v1.VolumeSource{
								Secret: &v1.SecretVolumeSource{
									SecretName: o.getSecretName(),
								},
							},
						},
						{
							Name: "weka-trace-routing",
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{
										Name: o.getWekaRoutingKeyName(),
									},
								},
							},
						},
						{
							Name: "k8s-trace-routing",
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{
										Name: o.getK8sRoutingKeyName(),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	err := ctrl.SetControllerReference(o.ownerRef, &deployment, o.mgr.GetScheme())
	if err != nil {
		return err
	}

	err = o.mgr.GetClient().Create(ctx, &deployment)
	if err != nil {
		if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
			//fetch current deployment
			err = o.mgr.GetClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
			if err != nil {
				return err
			}
			o.deployment = &deployment
			return nil
		}
		return err
	}
	o.deployment = &deployment
	return nil
}

func (o *MaintainTraceSession) EnsureSecret(ctx context.Context) error {
	// create secret object and apply it
	// generate token and store it in secret
	secret := v1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:        o.getSecretName(),
			Namespace:   o.ownerRef.GetNamespace(),
			Labels:      o.ownerRef.GetLabels(),
			Annotations: o.ownerRef.GetAnnotations(),
		},
		StringData: map[string]string{
			"token": util.GeneratePassword(128),
		},
	}
	err := ctrl.SetControllerReference(o.ownerRef, &secret, o.mgr.GetScheme())
	if err != nil {
		return err
	}

	err = o.mgr.GetClient().Create(ctx, &secret)
	if err != nil {
		if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
			//fetch current secret
			err = o.mgr.GetClient().Get(ctx, client.ObjectKey{Name: secret.Name, Namespace: secret.Namespace}, &secret)
			if err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil

}

func (o *MaintainTraceSession) getSecretName() string {
	return "weka-trace-session-" + o.ownerRef.GetName()
}

type ContainerInfo struct {
	Name                string `json:"name"`
	TraceServerEndpoint string `json:"trace_server_endpoint"`
}

type TraceServerDiscovery struct {
	Nodes      map[int]NodeInfo      `json:"nodes"`
	Containers map[int]ContainerInfo `json:"containers"`
}

type PodDiscovery struct {
	Pods map[string]ContainerInfo `json:"pods"`
}

type NodeInfo struct {
	Slot        int `json:"slot"`
	ContainerID int `json:"container_id"`
}

//	func GetTraceInfoForContainer(ctx context.Context, container weka.WekaContainer) (*ContainerInfo, error) {
//		services.NewWekaService()
//		// fetch container info from config map
//	}
func (o *MaintainTraceSession) EnsureWekaNodeRoutingConfigMap(ctx context.Context) error {
	// this config map serves as simplified discovery mechanism, this one contains map of weka node id to weka container id
	// in addition to that it contains map of container id to container name + base port + trace server port
	// this way trace viewer proxy can discover trace server for given container
	wekaService, err := o.getWekaService(ctx)
	if err != nil {
		return err
	}

	processes, err := wekaService.ListProcesses(ctx, services.ProcessListOptions{})
	if err != nil {
		return err
	}

	data := &TraceServerDiscovery{
		Nodes:      make(map[int]NodeInfo),
		Containers: make(map[int]ContainerInfo),
	}

	for _, process := range processes {
		nodeId, err := resources.NodeIdToInt(process.NodeId)
		if err != nil {
			return err
		}
		containerId, err := resources.HostIdToContainerId(process.NodeInfo.HostId)
		if err != nil {
			return err
		}
		data.Nodes[nodeId] = NodeInfo{
			Slot:        process.NodeInfo.Slot,
			ContainerID: containerId,
		}
		if _, ok := data.Containers[containerId]; !ok {
			data.Containers[containerId] = ContainerInfo{
				Name:                process.NodeInfo.ContainerName,
				TraceServerEndpoint: process.NodeInfo.ManagementIps[0] + ":" + strconv.Itoa(process.NodeInfo.ManagementPort+50),
			}
		}
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	configMap := v1.ConfigMap{
		ObjectMeta: ctrl.ObjectMeta{
			Name:        o.getWekaRoutingKeyName(),
			Namespace:   o.ownerRef.GetNamespace(),
			Labels:      o.ownerRef.GetLabels(),
			Annotations: o.ownerRef.GetAnnotations(),
		},
		BinaryData: map[string][]byte{
			"trace-routing.json": jsonBytes,
		},
	}

	err = ctrl.SetControllerReference(o.ownerRef, &configMap, o.mgr.GetScheme())
	if err != nil {
		return err
	}

	err = o.mgr.GetClient().Create(ctx, &configMap)
	if err != nil {
		if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
			//fetch current config map
			err = o.mgr.GetClient().Get(ctx, client.ObjectKey{Name: configMap.Name, Namespace: configMap.Namespace}, &configMap)
			if err != nil {
				return err
			}
			// update current config map
			configMap.BinaryData["trace-routing.json"] = jsonBytes
			err = o.mgr.GetClient().Update(ctx, &configMap)
			// TODO: check if we need to update config map to begin with, while json wont maintain order of elements, original struct should
			if err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (o *MaintainTraceSession) getWekaRoutingKeyName() string {
	return "weka-trace-routing-" + o.ownerRef.GetName()
}

func (o *MaintainTraceSession) getK8sRoutingKeyName() string {
	return "weka-k8s-trace-routing-" + o.ownerRef.GetName()
}

func (o *MaintainTraceSession) EnsureK8sContainerRoutingConfigMap(ctx context.Context) error {
	// another discovery mechanism, this one will map k8s pod name to endpoint of trace server that serves that pod
	err := o.ensureClusterContainers(ctx)
	if err != nil {
		return err
	}
	data := &PodDiscovery{
		Pods: make(map[string]ContainerInfo),
	}
	for _, container := range o.containers {
		mggmtIps := container.Status.GetManagementIps()
		if len(mggmtIps) == 0 {
			err = fmt.Errorf("no management ips found for container %s", container.Name)
			return err
		}
		data.Pods[container.Name] = ContainerInfo{
			Name:                container.Spec.WekaContainerName,
			TraceServerEndpoint: mggmtIps[0] + ":" + strconv.Itoa(container.GetPort()+50),
		}
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	configMap := v1.ConfigMap{
		ObjectMeta: ctrl.ObjectMeta{
			Name:        o.getK8sRoutingKeyName(),
			Namespace:   o.ownerRef.GetNamespace(),
			Labels:      o.ownerRef.GetLabels(),
			Annotations: o.ownerRef.GetAnnotations(),
		},
		BinaryData: map[string][]byte{
			"k8s-routing.json": jsonBytes,
		},
	}

	err = ctrl.SetControllerReference(o.ownerRef, &configMap, o.mgr.GetScheme())
	if err != nil {
		return err
	}

	err = o.mgr.GetClient().Create(ctx, &configMap)
	if err != nil {
		if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
			//fetch current config map
			err = o.mgr.GetClient().Get(ctx, client.ObjectKey{Name: configMap.Name, Namespace: configMap.Namespace}, &configMap)
			if err != nil {
				return err
			}
			// update current config map
			configMap.BinaryData["k8s-routing.json"] = jsonBytes
			err = o.mgr.GetClient().Update(ctx, &configMap)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *MaintainTraceSession) ensureClusterContainers(ctx context.Context) error {
	if len(o.containers) == 0 {
		o.containers = discovery.GetClusterContainers(ctx, o.mgr.GetClient(), o.cluster, "")
	}
	if len(o.containers) == 0 {
		return errors.New("no cluster containers found")
	}
	return nil
}

func (o *MaintainTraceSession) WaitTillExpiration(ctx context.Context) error {
	// If duration is 0 or omitted, keep waiting
	if o.payload.Duration.Duration == 0 {
		return lifecycle.NewWaitErrorWithDuration(
			errors.New("continuous trace session - keeping status Running"),
			5*time.Minute,
		)
	}

	startTime := o.ownerRef.GetCreationTimestamp()
	expirationTime := startTime.Add(o.payload.Duration.Duration)
	if meta.Now().After(expirationTime) {
		return nil
	}

	sleepFor := time.Minute
	if expirationTime.Sub(time.Now()) < sleepFor {
		sleepFor = expirationTime.Sub(time.Now())
	}
	return lifecycle.NewWaitErrorWithDuration(errors.New("waiting for session expiration"), sleepFor)
}

func (o *MaintainTraceSession) CleanupResources(ctx context.Context) error {
	namespace := o.ownerRef.GetNamespace()

	// Resources to delete
	resourcesToDelete := []client.Object{
		&apps.Deployment{
			ObjectMeta: meta.ObjectMeta{
				Name:      "trace-session-" + o.ownerRef.GetName(),
				Namespace: namespace,
			},
		},
		&v1.Secret{
			ObjectMeta: meta.ObjectMeta{
				Name:      o.getSecretName(),
				Namespace: namespace,
			},
		},
		&v1.ConfigMap{
			ObjectMeta: meta.ObjectMeta{
				Name:      o.getWekaRoutingKeyName(),
				Namespace: namespace,
			},
		},
		&v1.ConfigMap{
			ObjectMeta: meta.ObjectMeta{
				Name:      o.getK8sRoutingKeyName(),
				Namespace: namespace,
			},
		},
	}

	// Delete each resource, ignoring all errors
	for _, res := range resourcesToDelete {
		if err := o.mgr.GetClient().Delete(ctx, res); err != nil && client.IgnoreNotFound(err) != nil {
			fmt.Printf("Failed to delete %s: %v\n", res.GetName(), err)
		}
	}

	return nil
}

func (o *MaintainTraceSession) getWekaService(ctx context.Context) (services.WekaService, error) {
	err := o.ensureClusterContainers(ctx)
	if err != nil {
		return nil, err
	}

	container, err := resources.SelectActiveContainerWithRole(ctx, o.containers, weka.WekaContainerModeCompute)
	if err != nil {
		return nil, err
	}

	execService := exec.NewExecService(o.restClient, o.mgr.GetConfig())
	return services.NewWekaService(execService, container), nil
}
