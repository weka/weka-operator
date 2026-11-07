package wekacontainer

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/weka-operator/internal/node_agent"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestGetActiveMountsInfoSkipOverrideIsUnknownWithNoMounts(t *testing.T) {
	c := &weka.WekaContainer{}
	c.Spec.Overrides = &weka.WekaContainerSpecOverrides{SkipActiveMountsCheck: true}
	r := &containerReconcilerLoop{container: c}

	info, err := r.getActiveMountsInfo(context.Background())
	if err != nil || info.mounts != 0 || info.frontend != frontendUnknown {
		t.Fatalf("got %+v, %v; want 0 mounts and unknown frontend", info, err)
	}
	mounts, err := r.GetActiveMounts(context.Background())
	if err != nil || mounts == nil || *mounts != 0 {
		t.Fatalf("GetActiveMounts = %v, %v; want 0", mounts, err)
	}
}

func TestResolveActiveMounts(t *testing.T) {
	yes := true
	for name, tc := range map[string]struct {
		resp    *node_agent.ActiveMountsResponse
		err     error
		want    activeMountsInfo
		wantErr bool
	}{
		"no driver: no mounts, not connected": {nil, &NoWekaFsDriverFound{}, activeMountsInfo{frontend: frontendNotConnected}, false},
		"wrapped no driver":                   {nil, errors.Join(errors.New("ctx"), &NoWekaFsDriverFound{}), activeMountsInfo{frontend: frontendNotConnected}, false},
		"connected":                           {&node_agent.ActiveMountsResponse{ActiveMounts: 2, FrontendConnected: &yes}, nil, activeMountsInfo{mounts: 2, frontend: frontendConnected}, false},
		"old node-agent: unknown":             {&node_agent.ActiveMountsResponse{ActiveMounts: 1}, nil, activeMountsInfo{mounts: 1, frontend: frontendUnknown}, false},
		"other failure":                       {nil, errors.New("boom"), activeMountsInfo{}, true},
	} {
		got, err := resolveActiveMounts(tc.resp, tc.err)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v, err=%v", name, got, err, tc.want, tc.wantErr)
		}
	}
}
