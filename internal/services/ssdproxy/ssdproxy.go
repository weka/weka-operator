package ssdproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

// PodNotFoundError is returned when no node-agent pod exists on a node.
type PodNotFoundError struct {
	NodeName string
}

func (e *PodNotFoundError) Error() string {
	return fmt.Sprintf("no node agent pod found on node %s", e.NodeName)
}

// PodNotRunningError is returned when a node-agent pod exists but is not yet running.
type PodNotRunningError struct{}

func (e *PodNotRunningError) Error() string {
	return "node agent pod exists but is not running"
}

// ImageMismatchError is returned when the node-agent image does not match the operator image.
type ImageMismatchError struct {
	NodeAgentImage string
	OperatorImage  string
}

func (e *ImageMismatchError) Error() string {
	return fmt.Sprintf("node agent image mismatch: node-agent has %s but operator has %s",
		e.NodeAgentImage, e.OperatorImage)
}

// Client talks to ssdproxy containers through their node's node-agent JSONRPC endpoint.
type Client struct {
	kubeService kubernetes.KubeService
}

// NewClient builds a Client over the given KubeService.
func NewClient(kubeService kubernetes.KubeService) *Client {
	return &Client{kubeService: kubeService}
}

// token cache shared across Client instances (the token is operator-wide).
var (
	tokenMu       sync.Mutex
	cachedToken   string
	tokenLastPull time.Time
)

// GetNodeAgentPod returns the running node-agent pod on nodeName, validating its image
// matches the operator image. nodeName must be non-empty.
func (c *Client) GetNodeAgentPod(ctx context.Context, nodeName weka.NodeName) (*v1.Pod, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetNodeAgentPod", "node", nodeName)
	defer logger.End()

	if nodeName == "" {
		return nil, errors.New("nodeName is required")
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	pods, err := c.kubeService.GetPodsSimple(ctx, ns, string(nodeName), map[string]string{
		"control-plane": "weka-node-agent",
		"app":           "weka-node-agent",
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list node agent pods")
	}

	if len(pods) == 0 {
		return nil, &PodNotFoundError{NodeName: string(nodeName)}
	}

	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase == v1.PodRunning {
			if err := validateNodeAgentImage(pod); err != nil {
				return nil, err
			}
			return pod, nil
		}
	}

	return nil, &PodNotRunningError{}
}

// GetNodeAgentToken returns the node-agent auth token, cached for up to a minute.
func (c *Client) GetNodeAgentToken(ctx context.Context) (string, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetNodeAgentToken")
	defer logger.End()

	tokenMu.Lock()
	defer tokenMu.Unlock()

	if cachedToken != "" && time.Since(tokenLastPull) < time.Minute {
		return cachedToken, nil
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return "", err
	}

	secret, err := c.kubeService.GetSecret(ctx, config.Config.Metrics.NodeAgentSecretName, ns)
	if err != nil {
		return "", err
	}
	if secret == nil {
		return "", errors.New("no node agent secret found")
	}

	token := string(secret.Data["token"])
	if token == "" {
		return "", errors.New("no token found in node agent secret")
	}

	cachedToken = token
	tokenLastPull = time.Now()
	return token, nil
}

func getNodeAgentContainerImage(pod *v1.Pod) (string, error) {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == "node-agent" {
			return pod.Spec.Containers[i].Image, nil
		}
	}
	return "", fmt.Errorf("node-agent container not found in pod %s", pod.Name)
}

func validateNodeAgentImage(pod *v1.Pod) error {
	nodeAgentImage, err := getNodeAgentContainerImage(pod)
	if err != nil {
		return errors.Wrap(err, "failed to get node-agent container image")
	}
	operatorImage := config.Config.OperatorImage
	if operatorImage != nodeAgentImage {
		return &ImageMismatchError{NodeAgentImage: nodeAgentImage, OperatorImage: operatorImage}
	}
	return nil
}

// jsonrpc calls method on the ssdproxy container through the node-agent of agentPod.
func jsonrpc(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, method string, params map[string]any, result any) error {
	return node_agent.ProxyJSONRPC(ctx, agentPod, token, node_agent.JSONRPCProxyPayload{
		ContainerId: ssdproxyContainerUUID,
		Method:      method,
		Params:      params,
	}, result)
}

// ListPhysicalDrives enumerates all physical drives on the ssdproxy via JSONRPC.
func (c *Client) ListPhysicalDrives(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID string) ([]PhysicalDrive, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ListPhysicalDrives")
	defer logger.End()

	var drives []PhysicalDrive
	err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_list_physical_drives", map[string]any{}, &drives)
	return drives, err
}

// ListVirtualDrivesByPhysicalUUID lists the virtual drives on a single physical drive.
func (c *Client) ListVirtualDrivesByPhysicalUUID(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, physicalUUID string) ([]VirtualDrive, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ListVirtualDrivesByPhysicalUUID", "physical_uuid", physicalUUID)
	defer logger.End()

	var drives []VirtualDrive
	err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_list_virtual_drives", map[string]any{
		"physicalUuid": physicalUUID,
	}, &drives)
	if err != nil {
		return nil, fmt.Errorf("failed to list virtual drives for physical drive %s: %w", physicalUUID, err)
	}

	// PhysicalUUID is not part of the JSONRPC response; populate it from the request.
	for i := range drives {
		drives[i].PhysicalUUID = physicalUUID
	}
	return drives, nil
}

// ListVirtualDrives enumerates every virtual drive across all physical drives on the ssdproxy.
// It queries physical drives first, then virtual drives for each physical drive that has any.
// This is the full-coverage scan — including physical drives that hold only orphaned VIDs.
func (c *Client) ListVirtualDrives(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID string) ([]VirtualDrive, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ListVirtualDrives")
	defer logger.End()

	physicalDrives, err := c.ListPhysicalDrives(ctx, agentPod, token, ssdproxyContainerUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to list physical drives: %w", err)
	}

	var all []VirtualDrive
	for _, pd := range physicalDrives {
		if pd.NumVirtualDrives == 0 {
			continue
		}
		vds, err := c.ListVirtualDrivesByPhysicalUUID(ctx, agentPod, token, ssdproxyContainerUUID, pd.PhysicalUUID)
		if err != nil {
			return nil, fmt.Errorf("failed to list virtual drives for physical drive %s: %w", pd.PhysicalUUID, err)
		}
		all = append(all, vds...)
	}

	logger.Info("Listed all virtual drives across physical drives",
		"total_virtual_drives", len(all), "physical_drives", len(physicalDrives))
	return all, nil
}

// AddVirtualDrive adds a virtual drive via ssd_proxy_add_virtual_drive and returns the VID. idParam is
// "virtualUuid" (legacy proxy) or "label" (proxy with ssd_proxy_vid_label_add, which derives the VID
// from the label; re-adding a label whose VID is still active returns that same VID).
func (c *Client) AddVirtualDrive(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, idParam, id, physicalUUID, clusterGUID string, sizeGiB int) (string, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "AddVirtualDrive", idParam, id, "physical_uuid", physicalUUID)
	defer logger.End()

	var vid string
	err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_add_virtual_drive", map[string]any{
		idParam:        id,
		"physicalUuid": physicalUUID,
		"clusterGuid":  clusterGUID,
		"sizeGiB":      sizeGiB,
	}, &vid)
	if err != nil {
		return "", err
	}
	if vid == "" {
		return "", errors.New("JSONRPC reply has no VID")
	}
	return vid, nil
}

// RemoveVirtualDrive removes a single virtual drive via ssd_proxy_remove_virtual_drive, which returns
// a bool. It treats both a JSONRPC error object and a result:false reply as failure, except a VID the
// proxy does not hold, which counts as already removed.
func (c *Client) RemoveVirtualDrive(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, virtualUUID string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RemoveVirtualDrive", "virtual_uuid", virtualUUID)
	defer logger.End()

	result, err := removeVirtualDrive(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_remove_virtual_drive", map[string]any{"virtualUuid": virtualUUID})
	if err != nil {
		return err
	}
	if string(result) == "false" {
		return fmt.Errorf("JSONRPC call failed: result is false")
	}
	return nil
}

// RemoveVirtualDriveByLabel removes the active VID the proxy derived from label via
// ssd_proxy_remove_label, for an allocation whose VID was never persisted. Only valid on proxies with
// ssd_proxy_vid_label_add.
func (c *Client) RemoveVirtualDriveByLabel(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, label, physicalUUID string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RemoveVirtualDriveByLabel", "label", label, "physical_uuid", physicalUUID)
	defer logger.End()

	result, err := removeVirtualDrive(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_remove_label", map[string]any{
		"physicalUuid": physicalUUID,
		"label":        label,
	})
	if err != nil {
		return err
	}
	var vid string
	_ = json.Unmarshal(result, &vid) //nolint:errcheck // the VID is only logged
	logger.Info("Removed virtual drive by label", "virtual_uuid", vid)
	return nil
}

// removeVirtualDrive calls a proxy remove method and returns its raw result, nil when the proxy did
// not hold the drive.
func removeVirtualDrive(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, method string, params map[string]any) (json.RawMessage, error) {
	logger := instrumentation.CurrentSpanLogger(ctx)

	var result json.RawMessage
	err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, method, params, &result)
	var jrpcErr *node_agent.JSONRPCError
	if errors.As(err, &jrpcErr) && jrpcErr.IsException("VirtualDriveNotFoundException") {
		logger.Info("Virtual drive not found on proxy, treating as already removed")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	logger.Info("Virtual drive removed successfully via JSONRPC", "result", string(result))
	return result, nil
}
