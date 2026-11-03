package node_agent

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestJSONRPCHandlerUnregisteredContainer(t *testing.T) {
	tests := []struct {
		name string
		body string
		want func(int) bool
	}{
		{name: "without name", body: `{"container_id":"uid","method":"cluster_identity"}`, want: is(http.StatusNotFound)},
		{name: "with name", body: `{"container_id":"0b6c3f6e-3c1a-4c55-9a8e-2f1d5a7b9c10","method":"cluster_identity","weka_container_name":"drives0"}`, want: isNot(http.StatusNotFound, http.StatusBadRequest)},
		{name: "non-uuid id", body: `{"container_id":"../../x","method":"cluster_identity","weka_container_name":"drives0"}`, want: is(http.StatusBadRequest)},
		{name: "path in name", body: `{"container_id":"0b6c3f6e-3c1a-4c55-9a8e-2f1d5a7b9c10","method":"cluster_identity","weka_container_name":"../drives0"}`, want: is(http.StatusBadRequest)},
		{name: "dot-dot name", body: `{"container_id":"0b6c3f6e-3c1a-4c55-9a8e-2f1d5a7b9c10","method":"cluster_identity","weka_container_name":".."}`, want: is(http.StatusBadRequest)},
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
			if !tt.want(rec.Code) {
				t.Errorf("got status %d, body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func is(code int) func(int) bool { return func(c int) bool { return c == code } }

func isNot(codes ...int) func(int) bool {
	return func(c int) bool { return !slices.Contains(codes, c) }
}
