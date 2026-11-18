package node_agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/pkg/util"
)

// ProxyJSONRPC calls the node-agent /jsonrpc endpoint of agentPod and decodes the reply into result; a weka JSONRPC error comes back as *JSONRPCError.
func ProxyJSONRPC(ctx context.Context, agentPod *v1.Pod, token string, payload JSONRPCProxyPayload, result any) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSONRPC payload: %w", err)
	}

	url := URL(agentPod.Status.PodIP, "/jsonrpc")
	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return fmt.Errorf("failed to call node agent /jsonrpc endpoint: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // error return value intentionally not checked

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read JSONRPC response body: %w", err)
	}

	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *JSONRPCError   `json:"error"`
	}
	decodeErr := json.Unmarshal(body, &reply)

	if resp.StatusCode != http.StatusOK {
		if decodeErr == nil && reply.Error != nil {
			return reply.Error
		}
		return fmt.Errorf("node agent returned status %d: %s", resp.StatusCode, string(body))
	}
	if decodeErr != nil {
		return fmt.Errorf("failed to parse JSONRPC response: %w, body: %s", decodeErr, string(body))
	}
	if result != nil {
		if err := json.Unmarshal(reply.Result, result); err != nil {
			return fmt.Errorf("failed to decode JSONRPC result: %w, body: %s", err, string(body))
		}
	}
	return nil
}
