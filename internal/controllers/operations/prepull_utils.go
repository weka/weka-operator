package operations

import (
	"context"
	"crypto/sha256"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	PrePullLabelApp              = "app"
	PrePullLabelAppValue         = "weka-image-prepull"
	PrePullLabelOwnerUID         = "weka.io/prepull-owner-uid"
	PrePullLabelOwnerKind        = "weka.io/prepull-owner-kind"
	PrePullLabelOwnerName        = "weka.io/prepull-owner-name"
	PrePullLabelOwnerNamespace   = "weka.io/prepull-owner-namespace"
	PrePullLabelImageHash        = "weka.io/prepull-image-hash"
	PrePullAnnotationTargetImage = "weka.io/prepull-target-image"
)

// PrePullDaemonSetConfig contains configuration for building a pre-pull DaemonSet
type PrePullDaemonSetConfig struct {
	Name            string
	Namespace       string
	OwnerUID        string
	OwnerKind       string
	OwnerName       string
	OwnerNamespace  string
	TargetImage     string
	ImagePullSecret string
	NodeSelector    map[string]string
	Tolerations     []corev1.Toleration
}

// GetPrePullImageHash returns a short hash of the image for use in naming
func GetPrePullImageHash(image string) string {
	hash := sha256.Sum256([]byte(image))
	return fmt.Sprintf("%x", hash)[:8]
}

// GetPrePullDaemonSetName returns the DaemonSet name for a given owner and image
// Format: prepull-{ownerName}-{imageHash[:8]} (max 63 chars)
func GetPrePullDaemonSetName(ownerName, targetImage string) string {
	imageHash := GetPrePullImageHash(targetImage)
	name := fmt.Sprintf("prepull-%s-%s", ownerName, imageHash)
	if len(name) > 63 {
		// Truncate owner name to fit within 63 chars
		maxOwnerLen := max(63-len("prepull--")-len(imageHash), 1)
		name = fmt.Sprintf("prepull-%s-%s", ownerName[:maxOwnerLen], imageHash)
	}
	return name
}

func BuildPrePullDaemonSet(cfg PrePullDaemonSetConfig) *appsv1.DaemonSet {
	imageHash := GetPrePullImageHash(cfg.TargetImage)

	labels := map[string]string{
		PrePullLabelApp:            PrePullLabelAppValue,
		PrePullLabelOwnerUID:       cfg.OwnerUID,
		PrePullLabelOwnerKind:      cfg.OwnerKind,
		PrePullLabelOwnerName:      cfg.OwnerName,
		PrePullLabelOwnerNamespace: cfg.OwnerNamespace,
		PrePullLabelImageHash:      imageHash,
		// Required label for resources created by the operator
		// NOTE: used by cleanup and later for pods caching
		"app.kubernetes.io/created-by": "weka-operator",
	}

	annotations := map[string]string{
		PrePullAnnotationTargetImage: cfg.TargetImage,
	}

	// Init container pulls the target image by running a simple command
	initContainer := corev1.Container{
		Name:            "pull-image",
		Image:           cfg.TargetImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c", "echo 'Image pulled successfully'"},
	}

	// Main container keeps pod alive for status tracking
	mainContainer := corev1.Container{
		Name:            "status-holder",
		Image:           config.Config.MaintenanceImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sleep", "infinity"},
	}

	imagePullSecrets := []corev1.LocalObjectReference{
		{Name: config.Config.MaintenanceImagePullSecret},
	}
	if cfg.ImagePullSecret != "" {
		imagePullSecrets = []corev1.LocalObjectReference{
			{Name: cfg.ImagePullSecret},
		}
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cfg.Name,
			Namespace:   cfg.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					PrePullLabelApp:       PrePullLabelAppValue,
					PrePullLabelImageHash: imageHash,
					PrePullLabelOwnerUID:  cfg.OwnerUID,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					InitContainers:   []corev1.Container{initContainer},
					Containers:       []corev1.Container{mainContainer},
					ImagePullSecrets: imagePullSecrets,
					NodeSelector:     cfg.NodeSelector,
					Tolerations:      cfg.Tolerations,
				},
			},
		},
	}

	return ds
}

// PrePullNodeStatus represents the pre-pull status for a single node
type PrePullNodeStatus struct {
	NodeName string
	PodName  string
	Ready    bool
	Reason   string // Why not ready (e.g., "PodPending", "InitContainerFailed", "NoPod")
}

// PrePullStatusResult contains the aggregate pre-pull status
type PrePullStatusResult struct {
	TotalNodes   int
	ReadyNodes   int
	PendingNodes int
	FailedNodes  int
	NodeStatuses []PrePullNodeStatus
	AllReady     bool
}

// GetTargetNodes returns nodes that match the given selectors and tolerations
// Nodes must be Ready and not Unschedulable
func GetTargetNodes(ctx context.Context, c client.Client, nodeSelector map[string]string, tolerations []corev1.Toleration) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	listOpts := []client.ListOption{}
	if len(nodeSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(nodeSelector))
	}

	if err := c.List(ctx, nodeList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var targetNodes []corev1.Node
	for _, node := range nodeList.Items {
		// Skip unschedulable nodes
		if node.Spec.Unschedulable {
			continue
		}
		// Skip nodes that are not Ready
		if NodeNotReady(&node) {
			continue
		}
		// Check if tolerations match node taints
		if !util.CheckTolerations(node.Spec.Taints, tolerations, nil) {
			continue
		}

		targetNodes = append(targetNodes, node)
	}

	return targetNodes, nil
}

// CheckPrePullStatus checks the pre-pull status for all target nodes
func CheckPrePullStatus(ctx context.Context, c client.Client, ds *appsv1.DaemonSet, targetNodes []corev1.Node) (PrePullStatusResult, error) {
	result := PrePullStatusResult{
		TotalNodes: len(targetNodes),
	}

	if len(targetNodes) == 0 {
		result.AllReady = true
		return result, nil
	}

	// List pods for this DaemonSet
	podList := &corev1.PodList{}
	if err := c.List(ctx, podList, client.InNamespace(ds.Namespace), client.MatchingLabels(ds.Spec.Selector.MatchLabels)); err != nil {
		return result, fmt.Errorf("failed to list pods: %w", err)
	}

	// Build a map of node -> pod
	nodeToPod := make(map[string]*corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName != "" {
			nodeToPod[pod.Spec.NodeName] = pod
		}
	}

	// Check status for each target node
	for _, node := range targetNodes {
		status := PrePullNodeStatus{
			NodeName: node.Name,
		}

		pod, exists := nodeToPod[node.Name]
		if !exists {
			status.Ready = false
			status.Reason = "NoPod"
			result.PendingNodes++
		} else {
			status.PodName = pod.Name
			// Check if pod is running (init containers completed)
			switch pod.Status.Phase {
			case corev1.PodRunning:
				status.Ready = true
				result.ReadyNodes++
			case corev1.PodFailed:
				status.Ready = false
				status.Reason = "PodFailed"
				result.FailedNodes++
			default:
				status.Ready = false
				status.Reason = fmt.Sprintf("Pod%s", string(pod.Status.Phase))
				result.PendingNodes++
			}
		}

		result.NodeStatuses = append(result.NodeStatuses, status)
	}

	result.AllReady = result.ReadyNodes == result.TotalNodes

	return result, nil
}

// ListPrePullDaemonSets lists all pre-pull DaemonSets owned by a specific owner
func ListPrePullDaemonSets(ctx context.Context, c client.Client, namespace, ownerUID string) ([]appsv1.DaemonSet, error) {
	dsList := &appsv1.DaemonSetList{}
	if err := c.List(ctx, dsList, client.InNamespace(namespace), client.MatchingLabels{
		PrePullLabelApp:      PrePullLabelAppValue,
		PrePullLabelOwnerUID: ownerUID,
	}); err != nil {
		return nil, fmt.Errorf("failed to list DaemonSets: %w", err)
	}

	return dsList.Items, nil
}
