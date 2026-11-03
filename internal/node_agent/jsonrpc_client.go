package node_agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/pkg/util"
)

// ProxyJSONRPC POSTs payload to the node-agent /jsonrpc endpoint of agentPod and returns the raw body.
func ProxyJSONRPC(ctx context.Context, agentPod *v1.Pod, token string, payload JSONRPCProxyPayload) (body []byte, statusCode int, err error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal JSONRPC payload: %w", err)
	}

	url := URL(agentPod.Status.PodIP, "/jsonrpc")
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
