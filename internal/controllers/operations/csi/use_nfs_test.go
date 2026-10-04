package csi

import (
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func containerArgs(t *testing.T, args []string) {
	t.Helper()
	// The flag's value is $(WEKAFS_CONTAINER_NAME). If it is rendered while the env var is not,
	// Kubernetes performs no substitution and the plugin receives the literal text as a container
	// name.
	for _, a := range args {
		if strings.Contains(a, "$(WEKAFS_CONTAINER_NAME)") {
			t.Errorf("rendered an unsubstitutable arg %q with no WEKAFS_CONTAINER_NAME env var", a)
		}
		if strings.HasPrefix(a, "--wekafscontainername") {
			t.Errorf("unexpected %q with no weka client container", a)
		}
	}
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}

func nfsWekaClient() *weka.WekaClient {
	wc := testWekaClient(nil)
	wc.Spec.UseNfs = true
	return wc
}

func TestUseNfs_RendersUseNfsFlagAndHostNetwork(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi
	nfs := nfsWekaClient()
	native := testWekaClient(nil)

	for _, tc := range []struct {
		name string
		wc   *weka.WekaClient
		want bool
	}{{"useNfs", nfs, true}, {"native", native, false}} {
		dep, err := NewCsiControllerDeployment(t.Context(), "csi", tc.wc, settings)
		if err != nil {
			t.Fatalf("NewCsiControllerDeployment: %v", err)
		}
		ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", tc.wc, tc.wc.Name, tc.wc.Namespace, nil, settings)
		if err != nil {
			t.Fatalf("NewCsiNodeDaemonSet: %v", err)
		}
		if got := hasArg(dep.Spec.Template.Spec.Containers[0].Args, "--usenfs"); got != tc.want {
			t.Errorf("%s: controller --usenfs = %v, want %v", tc.name, got, tc.want)
		}
		if got := hasArg(ds.Spec.Template.Spec.Containers[0].Args, "--usenfs"); got != tc.want {
			t.Errorf("%s: node plugin --usenfs = %v, want %v", tc.name, got, tc.want)
		}
		if got := dep.Spec.Template.Spec.HostNetwork; got != tc.want {
			t.Errorf("%s: controller hostNetwork = %v, want %v", tc.name, got, tc.want)
		}
		if got := hasArg(dep.Spec.Template.Spec.Containers[0].Args, "--manage-nfs-permissions=false"); got != tc.want {
			t.Errorf("%s: controller --manage-nfs-permissions=false = %v, want %v", tc.name, got, tc.want)
		}
		if got := hasArg(ds.Spec.Template.Spec.Containers[0].Args, "--manage-nfs-permissions=false"); got != tc.want {
			t.Errorf("%s: node plugin --manage-nfs-permissions=false = %v, want %v", tc.name, got, tc.want)
		}
		if got := ds.Spec.Template.Spec.HostNetwork; got != tc.want {
			t.Errorf("%s: node plugin hostNetwork = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUseNfs_OmitsWekafsContainerNameFlagAndEnv(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi
	wc := nfsWekaClient()

	dep, err := NewCsiControllerDeployment(t.Context(), "csi", wc, settings)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}
	plugin := dep.Spec.Template.Spec.Containers[0]
	containerArgs(t, plugin.Args)
	if hasEnv(plugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Error("controller must omit WEKAFS_CONTAINER_NAME for a useNfs client")
	}

	ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", wc, wc.Name, wc.Namespace, nil, settings)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	nodePlugin := ds.Spec.Template.Spec.Containers[0]
	containerArgs(t, nodePlugin.Args)
	if hasEnv(nodePlugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Error("node plugin must omit WEKAFS_CONTAINER_NAME for a useNfs client")
	}
}

func TestEmbeddedPath_RendersWekafsContainerNameAndEnvTogether(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi
	wc := testWekaClient(nil)

	dep, err := NewCsiControllerDeployment(t.Context(), "csi", wc, settings)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}
	plugin := dep.Spec.Template.Spec.Containers[0]
	if !hasArg(plugin.Args, "--wekafscontainername=$(WEKAFS_CONTAINER_NAME)") {
		t.Errorf("controller lost --wekafscontainername, args=%v", plugin.Args)
	}
	if !hasEnv(plugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Error("controller lost WEKAFS_CONTAINER_NAME")
	}

	ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", wc, wc.Name, wc.Namespace, nil, settings)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	nodePlugin := ds.Spec.Template.Spec.Containers[0]
	if !hasArg(nodePlugin.Args, "--wekafscontainername=$(WEKAFS_CONTAINER_NAME)") {
		t.Errorf("node plugin lost --wekafscontainername, args=%v", nodePlugin.Args)
	}
	if !hasEnv(nodePlugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Error("node plugin lost WEKAFS_CONTAINER_NAME")
	}
}

func TestUseNfs_ManageNodeTopologyLabels(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi

	on, err := NewCsiNodeDaemonSet(t.Context(), "csi", nfsWekaClient(), "clients", "default", nil, settings)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	if !hasArg(on.Spec.Template.Spec.Containers[0].Args, "--managenodetopologylabels") {
		t.Error("useNfs client must manage its own topology labels, or volumes never schedule")
	}

	off, err := NewCsiNodeDaemonSet(t.Context(), "csi", testWekaClient(nil), "clients", "default", nil, settings)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	if hasArg(off.Spec.Template.Spec.Containers[0].Args, "--managenodetopologylabels") {
		t.Error("embedded path must not manage topology labels: the operator already stamps them")
	}
}

// With no retain label there is nothing to retain for, so the affinity must not reference one.
func TestUseNfs_NoRetainAffinityTerm(t *testing.T) {
	wc := nfsWekaClient()
	wc.Spec.NodeSelector = map[string]string{"role": "worker"}

	ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", wc, "clients", "default", nil, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	aff := ds.Spec.Template.Spec.Affinity
	if aff == nil || aff.NodeAffinity == nil || aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatal("expected node affinity from the selector")
	}
	terms := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 {
		t.Fatalf("want a single term with no retain alternative, got %d", len(terms))
	}
	for _, expr := range terms[0].MatchExpressions {
		if strings.HasPrefix(expr.Key, "weka.io/csi-node-retain") {
			t.Errorf("unexpected retain key %q", expr.Key)
		}
	}
}

// A transport change must reach the hash, or the workloads would keep running the old args.
func TestUseNfsFlagChangesHash(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi

	a, err := GetCsiNodeDaemonSetHash("csi", testWekaClient(nil), "clients", "default", settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b, err := GetCsiNodeDaemonSetHash("csi", nfsWekaClient(), "clients", "default", settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if a == b {
		t.Error("useNfs did not reach the pod-template hash: the DaemonSet would keep its old args")
	}

	ca, err := GetCsiControllerDeploymentHash("csi", testWekaClient(nil), settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cb, err := GetCsiControllerDeploymentHash("csi", nfsWekaClient(), settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if ca == cb {
		t.Error("useNfs did not reach the controller pod-template hash")
	}
}

// A useNfs=false client must hash identically to a WekaClient with no UseNfs field at all: adding
// the field must not roll every existing CSI installation.
func TestUseNfsFalse_HashUnchanged(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi

	plain := testWekaClient(nil)
	explicitFalse := testWekaClient(nil)
	explicitFalse.Spec.UseNfs = false

	a, err := GetCsiNodeDaemonSetHash("csi", plain, "clients", "default", settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b, err := GetCsiNodeDaemonSetHash("csi", explicitFalse, "clients", "default", settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if a != b {
		t.Error("UseNfs=false must hash identically to UseNfs unset")
	}

	ca, err := GetCsiControllerDeploymentHash("csi", plain, settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cb, err := GetCsiControllerDeploymentHash("csi", explicitFalse, settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if ca != cb {
		t.Error("UseNfs=false must hash identically to UseNfs unset (controller)")
	}
}

// Under UseNfs the controller runs on hostNetwork, so two replicas on one node would collide on
// the same healthz/metrics host ports. Anti-affinity must keep them apart.
func TestControllerAntiAffinity_UseNfsAddsRequiredTerm(t *testing.T) {
	dep, err := NewCsiControllerDeployment(t.Context(), "csi", nfsWekaClient(), services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}

	aff := dep.Spec.Template.Spec.Affinity
	if aff == nil || aff.PodAntiAffinity == nil ||
		len(aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("expected a single required pod anti-affinity term, got %+v", aff)
	}
	term := aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	if term.TopologyKey != "kubernetes.io/hostname" {
		t.Errorf("TopologyKey = %q, want kubernetes.io/hostname", term.TopologyKey)
	}
	if term.LabelSelector == nil || !mapsEqual(term.LabelSelector.MatchLabels, dep.Spec.Selector.MatchLabels) {
		t.Errorf("anti-affinity selector %+v must match the deployment's own pod labels %+v",
			term.LabelSelector, dep.Spec.Selector.MatchLabels)
	}
}

// Without UseNfs the rendered spec must be unchanged: no affinity at all.
func TestControllerAntiAffinity_AbsentWithoutUseNfs(t *testing.T) {
	dep, err := NewCsiControllerDeployment(t.Context(), "csi", testWekaClient(nil), services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}
	if dep.Spec.Template.Spec.Affinity != nil {
		t.Errorf("expected no affinity without UseNfs, got %+v", dep.Spec.Template.Spec.Affinity)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
