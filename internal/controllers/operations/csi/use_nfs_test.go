package csi

import (
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/services"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// csiOnlyParams is what a standalone installation looks like: no weka client container anywhere,
// so no container name, no retain label, and the plugin owns its own topology labels.
func csiOnlyParams() *DeploymentParams {
	return &DeploymentParams{
		CsiGroup:                 "extnfs",
		OwnerName:                "operator-configuration",
		OwnerNamespace:           "weka-operator-system",
		OwnerUID:                 "uid-1",
		WekafsContainerName:      "",
		ManageNodeTopologyLabels: true,
		NodeDaemonSetName:        GetCSINodeDaemonSetNameForClient("extnfs", "csionly", "default"),
		RetainLabel:              "",
		SecretRef:                client.ObjectKey{Name: "ext-weka-api", Namespace: "weka-operator-system"},
		CreateStorageClasses:     true,
		StorageClassFilesystem:   "default",
	}
}

func containerArgs(t *testing.T, args []string) {
	t.Helper()
	// The flag's value is $(WEKAFS_CONTAINER_NAME). If it is rendered while the env var is not,
	// Kubernetes performs no substitution and the plugin receives the literal text as a container
	// name — a failure that looks like a misconfigured cluster rather than a missing env var.
	for _, a := range args {
		if strings.Contains(a, "$(WEKAFS_CONTAINER_NAME)") {
			t.Errorf("rendered an unsubstitutable arg %q with no WEKAFS_CONTAINER_NAME env var", a)
		}
		if strings.HasPrefix(a, "--wekafscontainername") {
			t.Errorf("unexpected %q with no weka client container", a)
		}
	}
}

func envNames(env []corev1.EnvVar) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		out = append(out, e.Name)
	}
	return out
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}

func TestUseNfs_NodeDaemonSetOmitsWekafsContainerName(t *testing.T) {
	ds, err := NewCsiNodeDaemonSet(t.Context(), csiOnlyParams(), nil, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	plugin := ds.Spec.Template.Spec.Containers[0]

	containerArgs(t, plugin.Args)
	if hasEnv(plugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Errorf("WEKAFS_CONTAINER_NAME must be omitted, got env %v", envNames(plugin.Env))
	}
}

func TestUseNfs_ControllerOmitsWekafsContainerName(t *testing.T) {
	dep, err := NewCsiControllerDeployment(t.Context(), csiOnlyParams(), services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}
	plugin := dep.Spec.Template.Spec.Containers[0]

	containerArgs(t, plugin.Args)
	// Asserted separately from the args: an earlier version of this gated the flag but left the
	// env var behind, and a live cluster caught what this test did not.
	if hasEnv(plugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Errorf("WEKAFS_CONTAINER_NAME must be omitted, got env %v", envNames(plugin.Env))
	}
}

// The controller's counterpart to the DaemonSet negative control: with a container name, both the
// flag and the env var must be present.
func TestEmbeddedPath_ControllerRendersWekafsContainerNameAndEnvTogether(t *testing.T) {
	dep, err := NewCsiControllerDeployment(t.Context(), testParams("csi", testWekaClient(nil)), services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiControllerDeployment: %v", err)
	}
	plugin := dep.Spec.Template.Spec.Containers[0]

	if !hasArg(plugin.Args, "--wekafscontainername=$(WEKAFS_CONTAINER_NAME)") {
		t.Errorf("controller lost --wekafscontainername, args=%v", plugin.Args)
	}
	if !hasEnv(plugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Errorf("controller lost WEKAFS_CONTAINER_NAME, env=%v", envNames(plugin.Env))
	}
}

// The negative control: with a container name, flag and env var must BOTH appear. A test that only
// checks the empty case would pass if the feature were removed entirely.
func TestEmbeddedPath_RendersWekafsContainerNameAndEnvTogether(t *testing.T) {
	ds, err := NewCsiNodeDaemonSet(t.Context(), testParams("csi", testWekaClient(nil)), nil, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	plugin := ds.Spec.Template.Spec.Containers[0]

	if !hasArg(plugin.Args, "--wekafscontainername=$(WEKAFS_CONTAINER_NAME)") {
		t.Errorf("embedded path lost --wekafscontainername, args=%v", plugin.Args)
	}
	if !hasEnv(plugin.Env, "WEKAFS_CONTAINER_NAME") {
		t.Errorf("embedded path lost WEKAFS_CONTAINER_NAME, env=%v", envNames(plugin.Env))
	}
}

func TestUseNfs_ManageNodeTopologyLabels(t *testing.T) {
	on, err := NewCsiNodeDaemonSet(t.Context(), csiOnlyParams(), nil, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	if !hasArg(on.Spec.Template.Spec.Containers[0].Args, "--managenodetopologylabels") {
		t.Error("standalone install must manage its own topology labels, or volumes never schedule")
	}

	off, err := NewCsiNodeDaemonSet(t.Context(), testParams("csi", testWekaClient(nil)), nil, services.DefaultConfigurationSettings().Csi)
	if err != nil {
		t.Fatalf("NewCsiNodeDaemonSet: %v", err)
	}
	if hasArg(off.Spec.Template.Spec.Containers[0].Args, "--managenodetopologylabels") {
		t.Error("embedded path must not manage topology labels: the operator already stamps them")
	}
}

// With no retain label there is nothing to retain for, so the affinity must not reference one.
func TestUseNfs_NoRetainAffinityTerm(t *testing.T) {
	p := csiOnlyParams()
	p.NodeSelector = map[string]string{"role": "worker"}

	ds, err := NewCsiNodeDaemonSet(t.Context(), p, nil, services.DefaultConfigurationSettings().Csi)
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

// The two DaemonSet naming schemes must never collide. cleanupOldSharedCsiNodeDaemonSet deletes
// GetCSINodeDaemonSetName on sight, so a per-client DaemonSet sharing that name would be torn
// down by an unrelated client's migration step.
func TestDaemonSetNamesDoNotCollide(t *testing.T) {
	const group = "extnfs"
	shared := GetCSINodeDaemonSetName(group)
	perClient := GetCSINodeDaemonSetNameForClient(group, "extnfs", "extnfs")

	if shared == perClient {
		t.Errorf("shared and per-client names both render %q", shared)
	}
	for _, n := range []string{shared, perClient} {
		if len(n) > 63 {
			t.Errorf("name %q exceeds 63 chars", n)
		}
	}
}

// A transport change must reach the hash, or the workloads would keep running the old args.
func TestUseNfsHashDiffersFromEmbedded(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi

	only, err := GetCsiNodeDaemonSetHash(csiOnlyParams(), settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	embedded, err := GetCsiNodeDaemonSetHash(testParams("extnfs", testWekaClient(nil)), settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if only == embedded {
		t.Error("standalone and embedded installs must not share a pod-template hash")
	}
}

// A csiOnly WekaClient must produce exactly the standalone shape: no container name, no retain
// label, and the plugin owning its own topology labels. This is the single branch everything else
// in this file depends on, so assert it directly rather than only through rendered specs.
func TestParamsFromWekaClient_UseNfsFlipsTheThreeGates(t *testing.T) {
	wc := testWekaClient(nil)
	wc.Spec.UseNfs = true

	p := testParams("csi", wc)

	if p.WekafsContainerName != "" {
		t.Errorf("WekafsContainerName = %q, want empty: there is no client container", p.WekafsContainerName)
	}
	if !p.ManageNodeTopologyLabels {
		t.Error("useNfs must hand topology labels to the plugin, or volumes never schedule")
	}
	if p.RetainLabel != "" {
		t.Errorf("RetainLabel = %q, want empty: nothing mounts through a weka client", p.RetainLabel)
	}
}

// Flipping useNfs must roll the workloads: the rendered args change, so the hash has to.
func TestUseNfsFlagChangesHash(t *testing.T) {
	settings := services.DefaultConfigurationSettings().Csi

	plain := testWekaClient(nil)
	only := testWekaClient(nil)
	only.Spec.UseNfs = true

	a, err := GetCsiNodeDaemonSetHash(testParams("csi", plain), settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b, err := GetCsiNodeDaemonSetHash(testParams("csi", only), settings)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if a == b {
		t.Error("csiOnly did not reach the pod-template hash: the DaemonSet would keep its old args")
	}
}

// The default transport is native wekafs: no NFS flag at all. Everything else about NFS - which
// interface group, which client group, which protocol version - is the plugin's own default or a
// cluster prerequisite, so the operator renders nothing for it.
func TestMountProtocolArgs_Defaults(t *testing.T) {
	if args := mountProtocolArgs(&DeploymentParams{}); len(args) != 0 {
		t.Errorf("expected no transport flags by default, got %v", args)
	}
}

// --usenfs comes from the deployment shape, not from a setting: it is what an installation with
// no weka client containers needs, and nothing else can ask for it.
func TestMountProtocolArgs_ForceNfsIsDerived(t *testing.T) {
	if hasArg(mountProtocolArgs(&DeploymentParams{}), "--usenfs") {
		t.Error("--usenfs must not appear without ForceNfs")
	}
	if !hasArg(mountProtocolArgs(&DeploymentParams{ForceNfs: true}), "--usenfs") {
		t.Error("ForceNfs must render --usenfs")
	}
}

// An NFS mount happens in the node's network namespace and the cluster authorises the source
// address, so anything mounting over NFS needs host networking regardless of the operator-wide
// setting.
func TestCsiHostNetwork(t *testing.T) {
	if csiHostNetwork(&DeploymentParams{ForceNfs: true}) != true {
		t.Error("NFS transport must force host networking")
	}
}
