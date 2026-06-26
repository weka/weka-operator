package ssdproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
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

// jsonrpc POSTs a JSONRPC payload to the node-agent of agentPod and returns the raw body.
func jsonrpc(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, method string, params map[string]any) (body []byte, statusCode int, err error) {
	payload := node_agent.JSONRPCProxyPayload{
		ContainerId: ssdproxyContainerUUID,
		Method:      method,
		Params:      params,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal JSONRPC payload: %w", err)
	}

	url := "http://" + agentPod.Status.PodIP + ":8090/jsonrpc"
	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to call node agent /jsonrpc endpoint: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // error return value intentionally not checked

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read JSONRPC response body: %w", readErr)
	}

	return respBody, resp.StatusCode, nil
}

// ListPhysicalDrives enumerates all physical drives on the ssdproxy via JSONRPC.
func (c *Client) ListPhysicalDrives(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID string) ([]PhysicalDrive, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ListPhysicalDrives")
	defer logger.End()

	respBody, status, err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_list_physical_drives", map[string]any{})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("node agent returned non-OK status: %d, body: %s", status, string(respBody))
	}

	var jsonrpcResp physicalDrivesResponse
	if err := json.Unmarshal(respBody, &jsonrpcResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", err, string(respBody))
	}
	return jsonrpcResp.Result, nil
}

// ListVirtualDrivesByPhysicalUUID lists the virtual drives on a single physical drive.
func (c *Client) ListVirtualDrivesByPhysicalUUID(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, physicalUUID string) ([]VirtualDrive, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ListVirtualDrivesByPhysicalUUID", "physical_uuid", physicalUUID)
	defer logger.End()

	respBody, status, err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_list_virtual_drives", map[string]any{
		"physicalUuid": physicalUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list virtual drives for physical drive %s: %w", physicalUUID, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("node agent returned non-OK status: %d, body: %s", status, string(respBody))
	}

	var response virtualDrivesResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", err, string(respBody))
	}

	// PhysicalUUID is not part of the JSONRPC response; populate it from the request.
	for i := range response.Result {
		response.Result[i].PhysicalUUID = physicalUUID
	}
	return response.Result, nil
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

// RemoveVirtualDrive removes a single virtual drive via ssd_proxy_remove_virtual_drive.
// It treats both a JSONRPC error object and a result:false reply as failure.
func (c *Client) RemoveVirtualDrive(ctx context.Context, agentPod *v1.Pod, token, ssdproxyContainerUUID, virtualUUID string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RemoveVirtualDrive", "virtual_uuid", virtualUUID)
	defer logger.End()

	respBody, status, err := jsonrpc(ctx, agentPod, token, ssdproxyContainerUUID, "ssd_proxy_remove_virtual_drive", map[string]any{
		"virtualUuid": virtualUUID,
	})
	if err != nil {
		return err
	}

	logger.Info("JSONRPC response received", "status_code", status, "response", string(respBody))

	if status != http.StatusOK {
		return fmt.Errorf("node agent returned non-OK status: %d, body: %s", status, string(respBody))
	}

	var jsonrpcResp struct {
		Result interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &jsonrpcResp); err != nil {
		return fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", err, string(respBody))
	}

	if jsonrpcResp.Error != nil {
		return fmt.Errorf("JSONRPC error [%d]: %s", jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}
	if resultBool, ok := jsonrpcResp.Result.(bool); ok && !resultBool {
		return fmt.Errorf("JSONRPC call failed: result is false")
	}

	logger.Info("Virtual drive removed successfully via JSONRPC", "result", jsonrpcResp.Result)
	return nil
}
