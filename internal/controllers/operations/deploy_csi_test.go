package operations

import (
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
)

func TestNodeIsCsiAccessible(t *testing.T) {
	prev := config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning
	t.Cleanup(func() { config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = prev })

	yes, no := true, false

	for name, tc := range map[string]struct {
		status      weka.ContainerStatus
		connected   *bool
		internal    string
		preventFlag bool
		nodeDown    bool
		want        bool
	}{
		"connected":                             {weka.Running, &yes, "", true, false, true},
		"not connected, READY":                  {weka.Running, &no, "READY", true, false, false},
		"connected, stopped":                    {weka.Stopped, &yes, "READY", true, false, false},
		"flag off, connected, stopped":          {weka.Stopped, &yes, "", false, false, true},
		"flag off, not connected, stopped":      {weka.Stopped, &no, "READY", false, false, false},
		"connected, node NotReady":              {weka.Running, &yes, "", true, true, false},
		"unknown, READY":                        {weka.Running, nil, "READY", true, false, true},
		"unknown, READY, node NotReady":         {weka.Running, nil, "READY", true, true, false},
		"unknown, not READY":                    {weka.Running, nil, "NOT_READY", true, false, false},
		"unknown, empty internal status":        {weka.Running, nil, "", true, false, false},
		"flag off, unknown, not running, READY": {weka.Error, nil, "READY", false, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = tc.preventFlag
			c := &weka.WekaContainer{}
			c.Status.Status = tc.status
			c.Status.InternalStatus = tc.internal
			if got := NewCsiTopologyLabelsService("csi.weka.io", "node-1", !tc.nodeDown, tc.connected, c).nodeIsCsiAccessible(); got != tc.want {
				t.Fatalf("nodeIsCsiAccessible = %v, want %v", got, tc.want)
			}
		})
	}
}
