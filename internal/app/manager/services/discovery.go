package services

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DiscoveryNodeInfo struct {
	IsHt bool `json:"is_ht"`
}

const discoveryAnnotation = "k8s.weka.io/discovery.json"

func GetCluster(ctx context.Context, c client.Client, name v1alpha1.ObjectReference) (*v1alpha1.WekaCluster, error) {
	cluster := &v1alpha1.WekaCluster{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: name.Namespace,
		Name:      name.Name,
	}, cluster)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get cluster")
	}
	return cluster, nil
}

func GetJoinIps(ctx context.Context, c client.Client, cluster *v1alpha1.WekaCluster) ([]string, error) {
	containers := GetClusterContainers(ctx, c, cluster, "compute")
	if len(containers) == 0 {
		return nil, errors.New("No compute containers found")
	}

	joinIpPortPairs := []string{}
	for _, container := range containers {
		if container.Status.ManagementIP == "" {
			return nil, errors.New("Container missing management IP")
		}
		joinIpPortPairs = append(joinIpPortPairs, WrapIpv6Brackets(container.Status.ManagementIP)+":"+strconv.Itoa(container.Spec.Port))
	}
	return joinIpPortPairs, nil
}

func WrapIpv6Brackets(ip string) string {
	if util.IsIpv6(ip) {
		return "[" + ip + "]"
	}
	return ip
}

func GetClusterContainers(ctx context.Context, c client.Client, cluster *v1alpha1.WekaCluster, mode string) []*v1alpha1.WekaContainer {
	// partially moved to crd.go, need to move the rest
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClusterContainers", "cluster", cluster.Name, "mode", mode, "cluster_uid", string(cluster.UID))
	defer end()

	containersList := v1alpha1.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(cluster.UID)},
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}
	err := c.List(ctx, &containersList, listOpts...)
	logger.InfoWithStatus(codes.Ok, "Listed containers", "count", len(containersList.Items))

	if err != nil {
		return nil
	}

	containers := []*v1alpha1.WekaContainer{}
	for i := range containersList.Items {
		containers = append(containers, &containersList.Items[i])
	}
	return containers
}

type OwnerWekaObject struct {
	Image           string              `json:"image"`
	ImagePullSecret string              `json:"imagePullSecrets"`
	Tolerations     []corev1.Toleration `json:"tolerations"`
}

func EnsureNodeDiscovered(ctx context.Context, c client.Client, ownerDetails OwnerWekaObject, nodeName string, exec ExecService) error {
	node := &corev1.Node{}
	err := c.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		return err
	}

	// Check if node already has the discovery.json annotation
	if _, ok := node.Annotations[discoveryAnnotation]; ok {
		return nil
	}

	// FormCluster a WekaContainer with mode "discovery"
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}

	discoveryContainer := &v1alpha1.WekaContainer{
		ObjectMeta: v1.ObjectMeta{
			Name:      "weka-discovery-" + nodeName,
			Namespace: operatorNamespace,
		},
		Spec: v1alpha1.WekaContainerSpec{
			Mode:            v1alpha1.WekaContainerModeDiscovery,
			NodeAffinity:    nodeName,
			Image:           ownerDetails.Image,
			ImagePullSecret: ownerDetails.ImagePullSecret,
			Tolerations:     ownerDetails.Tolerations,
		},
	}
	// Fill in the necessary fields here
	err = c.Create(ctx, discoveryContainer)
	if err != nil {
		// if already exits error check the status of existing container
		if !errors2.IsAlreadyExists(err) {
			return err
		}
	}

	// Wait for the data to be available on this container
	err = c.Get(ctx, types.NamespacedName{Name: discoveryContainer.Name, Namespace: discoveryContainer.Namespace}, discoveryContainer)
	if err != nil {
		return err
	}

	executor, err := exec.GetExecutor(ctx, discoveryContainer)
	if err != nil {
		return err
	}
	stdout, _, err := executor.ExecNamed(ctx, "fetch_discovery", []string{"cat", "/tmp/weka-discovery.json"})
	if err != nil {
		return errors.Wrap(err, "Failed to read discovery.json")
	}

	discoveryNodeInfo := &DiscoveryNodeInfo{}
	err = json.Unmarshal(stdout.Bytes(), discoveryNodeInfo)
	if err != nil {
		return errors.Wrap(err, "Failed to unmarshal discovery.json")
	}
	// unmarshalling just for json validation

	// Update the node with data in annotation
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[discoveryAnnotation] = string(stdout.Bytes())
	err = c.Update(ctx, node)
	if err != nil {
		return err
	}

	// Delete WekaContainer
	err = c.Delete(ctx, discoveryContainer)
	if err != nil {
		return err
	}

	return nil
}

func GetNodeDiscovery(ctx context.Context, c client.Client, node string) (*DiscoveryNodeInfo, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetNodeDiscovery", "node", node)
	defer end()

	nodeInfo := DiscoveryNodeInfo{}
	nodeObj := &corev1.Node{}
	err := c.Get(ctx, types.NamespacedName{Name: node}, nodeObj)
	if err != nil {
		logger.SetError(err, "node not found")
		return nil, err
	}

	discoveryJson, ok := nodeObj.Annotations[discoveryAnnotation]
	if !ok {
		logger.SetError(errors.New("Discovery json not found"), "Discovery json not found")
		return nil, errors.New("Discovery json not found")
	}

	err = json.Unmarshal([]byte(discoveryJson), &nodeInfo)
	if err != nil {
		logger.SetError(err, "Failed to unmarshal discovery.json")
		return nil, err
	}

	return &nodeInfo, nil
}

func ResolveCpuPolicy(ctx context.Context, c client.Client, node string, cpuPolicy v1alpha1.CpuPolicy) (v1alpha1.CpuPolicy, error) {
	if cpuPolicy != v1alpha1.CpuPolicyAuto {
		return cpuPolicy, nil
	}

	nodeInfo, err := GetNodeDiscovery(ctx, c, node)
	if err != nil {
		return cpuPolicy, err
	}

	if nodeInfo == nil { // asserting just in case
		return cpuPolicy, errors.New("nil-node info, while no error on node discovery")
	}

	if nodeInfo.IsHt {
		cpuPolicy = v1alpha1.CpuPolicyDedicatedHT // for now just as a sane default for clients cases
	} else {
		cpuPolicy = v1alpha1.CpuPolicyDedicated
	}
	return cpuPolicy, nil
}
