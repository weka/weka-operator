package csi

import (
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testParams builds the params the embedded path would build for a client, so tests exercise the
// same constructor production does rather than hand-assembling a struct.
func testParams(group string, wc *weka.WekaClient) *DeploymentParams {
	return ParamsFromWekaClient(wc, group, CsiSecretForWekaClient(wc, group))
}

// The embedded path must never hand the plugin ownership of topology labels, and must always tell
// it which weka container to talk to. Both are what separate it from a standalone install.
func TestParamsFromWekaClient_EmbeddedInvariants(t *testing.T) {
	p := testParams("csi", testWekaClient(nil))

	if p.ManageNodeTopologyLabels {
		t.Error("embedded path must not set --managenodetopologylabels: the operator stamps those labels")
	}
	if p.WekafsContainerName == "" {
		t.Error("embedded path must always name the weka client container")
	}
	if p.RetainLabel == "" {
		t.Error("embedded path must carry a retain label so mounts can drain")
	}
	if p.NodeDaemonSetName != GetCSINodeDaemonSetNameForClient("csi", "clients", "default") {
		t.Errorf("unexpected DaemonSet name %q", p.NodeDaemonSetName)
	}
}

// A nil csiConfig must behave exactly like an absent Advanced block, which is the overwhelmingly
// common case in the field.
func TestParamsFromWekaClient_NilCsiConfig(t *testing.T) {
	p := testParams("csi", testWekaClient(nil))

	if p.Advanced != nil {
		t.Error("Advanced must stay nil when csiConfig is unset")
	}
	if p.DisableControllerCreation {
		t.Error("DisableControllerCreation must default false")
	}
}

// One WekaClient field drives both selectors today. Preserving that is a compatibility
// requirement, not a design preference: splitting them would change the rendered controller.
func TestParamsFromWekaClient_OneSelectorDrivesBoth(t *testing.T) {
	sel := map[string]string{"weka.io/supports-clients": "true"}
	p := testParams("csi", testWekaClient(sel))

	if len(p.NodeSelector) != 1 || len(p.ControllerNodeSelector) != 1 {
		t.Fatalf("selectors not propagated: node=%v controller=%v", p.NodeSelector, p.ControllerNodeSelector)
	}
	if p.NodeSelector["weka.io/supports-clients"] != "true" ||
		p.ControllerNodeSelector["weka.io/supports-clients"] != "true" {
		t.Error("both selectors must come from spec.nodeSelector")
	}
}

// The secret the storage classes point at: named after the target cluster when there is one, and
// after the resolved group otherwise. The fallback must use the RESOLVED group, since that is what
// the operation used before the refactor.
func TestCsiSecretForWekaClient(t *testing.T) {
	withCluster := testWekaClient(nil)
	withCluster.Spec.TargetCluster = weka.ObjectReference{Name: "cluster-dev", Namespace: "wekans"}
	if got := CsiSecretForWekaClient(withCluster, "ignored.group"); got != (client.ObjectKey{Name: "weka-csi-cluster-dev", Namespace: "wekans"}) {
		t.Errorf("target-cluster branch = %v", got)
	}

	noCluster := testWekaClient(nil)
	if got := CsiSecretForWekaClient(noCluster, "resolved.group"); got != (client.ObjectKey{Name: "weka-csi-resolved.group", Namespace: "default"}) {
		t.Errorf("fallback branch = %v", got)
	}
}

// Storage classes only exist where a target cluster does, as the operation's predicate required.
func TestParamsFromWekaClient_StorageClassesFollowTargetCluster(t *testing.T) {
	noCluster := testWekaClient(nil)
	if testParams("csi", noCluster).CreateStorageClasses {
		t.Error("no target cluster must mean no storage classes")
	}

	withCluster := testWekaClient(nil)
	withCluster.Spec.TargetCluster = weka.ObjectReference{Name: "cluster-dev", Namespace: "default"}
	if !ParamsFromWekaClient(withCluster, "csi", client.ObjectKey{}).CreateStorageClasses {
		t.Error("a target cluster must mean storage classes")
	}
}
