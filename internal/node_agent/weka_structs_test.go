package node_agent

import (
	"encoding/json"
	"testing"
)

func TestWekaStructs(t *testing.T) {
	rawData := "{\"result\":{\"NodeId<121>\":{\"cpu\":[{\"stats\":{\"CPU_UTILIZATION\":2.31515788776139919},\"resolution\":60,\"timestamp\":\"2024-12-01T20:57:00Z\"}]}},\"id\":11939751076724815893,\"jsonrpc\":\"2.0\"}"

	var data LocalCpuUtilizationResponse
	err := json.Unmarshal([]byte(rawData), &data)
	if err != nil {
		t.Fatalf("Failed to unmarshal data: %v", err)
	}
}
