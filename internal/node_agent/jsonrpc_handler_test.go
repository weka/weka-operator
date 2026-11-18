package node_agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJSONRPCHandlerUnregisteredContainer(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "without name", body: `{"container_id":"uid","method":"cluster_identity"}`, want: http.StatusNotFound},
		{name: "with name", body: `{"container_id":"0b6c3f6e-3c1a-4c55-9a8e-2f1d5a7b9c10","method":"cluster_identity","weka_container_name":"drives0"}`, want: http.StatusInternalServerError},
		{name: "non-uuid id", body: `{"container_id":"../../x","method":"cluster_identity","weka_container_name":"drives0"}`, want: http.StatusBadRequest},
		{name: "path in name", body: `{"container_id":"0b6c3f6e-3c1a-4c55-9a8e-2f1d5a7b9c10","method":"cluster_identity","weka_container_name":"../drives0"}`, want: http.StatusBadRequest},
		{name: "dot-dot name", body: `{"container_id":"0b6c3f6e-3c1a-4c55-9a8e-2f1d5a7b9c10","method":"cluster_identity","weka_container_name":".."}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &NodeAgent{
				token:          "tok",
				lastTokenPull:  time.Now(),
				containersData: containersData{data: map[string]*ContainerInfo{}},
				jsonrpcService: NewWekaJSONRPCService(),
			}
			req := httptest.NewRequest(http.MethodPost, "/jsonrpc", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Token tok")
			rec := httptest.NewRecorder()
			a.jsonrpcHandler(rec, req)
			if rec.Code != tt.want {
				t.Errorf("got status %d, body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
