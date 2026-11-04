package wekacontainer

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/weka-operator/internal/node_agent"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// The override concerns deleting the container: the mount count skips the node-agent, but the label's frontend
// state is still read from it (here a memoized failure and a memoized answer stand in for the node-agent).
func TestSkipOverrideOnlyAffectsTheMountCount(t *testing.T) {
	c := &weka.WekaContainer{}
	c.Spec.Overrides = &weka.WekaContainerSpecOverrides{SkipActiveMountsCheck: true}
	agentErr := errors.New("node-agent unreachable")

	failing := &containerReconcilerLoop{container: c, activeMountsErr: agentErr}
	if _, err := failing.getActiveMountsInfo(context.Background()); !errors.Is(err, agentErr) {
		t.Fatalf("the label path must not short-circuit on the override: err = %v", err)
	}
	mounts, err := failing.GetActiveMounts(context.Background())
	if err != nil || mounts == nil || *mounts != 0 {
		t.Fatalf("the mount count must skip the node-agent: %v, %v", mounts, err)
	}

	for _, want := range []frontendState{frontendConnected, frontendNotConnected} {
		answered := &containerReconcilerLoop{container: c, activeMountsInfo: &activeMountsInfo{mounts: 3, frontend: want}}
		if info, err := answered.getActiveMountsInfo(context.Background()); err != nil || info.frontend != want {
			t.Fatalf("got %+v, %v; want frontend %v from the node-agent", info, err, want)
		}
		if mounts, _ := answered.GetActiveMounts(context.Background()); *mounts != 0 {
			t.Fatalf("deletion must still see 0 mounts under the override, got %d", *mounts)
		}
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
