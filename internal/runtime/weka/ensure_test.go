package weka

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/process"
	v1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// fakeRunner records every command it's asked to run and answers by matching a substring
// against the command's joined path+args. Scenarios not covered by scripted() succeed with
// empty output.
type fakeRunner struct {
	calls    []process.Command
	scripted map[string]string // substring -> stdout
	fail     map[string]bool   // substring -> return error
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	text := c.Path + " " + strings.Join(c.Args, " ")
	for substr := range f.fail {
		if strings.Contains(text, substr) {
			return process.Result{}, os.ErrInvalid
		}
	}
	for substr, out := range f.scripted {
		if strings.Contains(text, substr) {
			return process.Result{Stdout: []byte(out)}, nil
		}
	}
	return process.Result{}, nil
}

// ranAny reports whether any recorded call's path+args contains substr.
func (f *fakeRunner) ranAny(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c.Path+" "+strings.Join(c.Args, " "), substr) {
			return true
		}
	}
	return false
}

func TestCheckResourcesJSON_BackendNeverRemoves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	in := &ContainerInput{Runner: r, Name: "backend0"}

	err := checkResourcesJSON(context.Background(), in, false, "", dir)
	if err == nil {
		t.Fatal("expected error when backend has no recoverable resources file")
	}
	if r.ranAny("rm") {
		t.Errorf("backend path issued a rm command: %v", r.calls)
	}
}

func TestRelinkLatestResourcesFile_NonEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, err := relinkLatestResourcesFile(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !recovered {
		t.Error("expected recovered=true for a non-empty resources.json")
	}
}

func TestRelinkLatestResourcesFile_NoCandidate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, err := relinkLatestResourcesFile(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered {
		t.Error("expected recovered=false with no weka-resources.*.json candidate")
	}
}

func TestRelinkLatestResourcesFile_RelinksNewestCandidate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "weka-resources.1.json")
	newer := filepath.Join(dir, "weka-resources.2.json")
	if err := os.WriteFile(older, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(older, now, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now, now); err != nil {
		t.Fatal(err)
	}

	recovered, err := relinkLatestResourcesFile(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !recovered {
		t.Fatal("expected recovered=true when a candidate exists")
	}
	link, err := os.Readlink(filepath.Join(dir, "resources.json"))
	if err != nil {
		t.Fatalf("resources.json is not a symlink: %v", err)
	}
	if link != "weka-resources.2.json" {
		t.Errorf("resources.json links to %q, want the newest candidate weka-resources.2.json", link)
	}
}

func TestShouldRecreateClientContainer_BasePortMismatch(t *testing.T) {
	res := []byte(`{"base_port":14000,"restricted_client":true}`)
	if !shouldRecreateClientContainer(15000, "4.2.7.65", res) {
		t.Error("expected recreate on base_port mismatch")
	}
}

func TestShouldRecreateClientContainer_RestrictedClient(t *testing.T) {
	res := []byte(`{"base_port":14000,"restricted_client":false}`)
	if !shouldRecreateClientContainer(14000, "4.2.7.65", res) {
		t.Error("expected recreate: non-4.2.7.64 image must have restricted_client=true")
	}

	res64 := []byte(`{"base_port":14000,"restricted_client":false}`)
	if shouldRecreateClientContainer(14000, "weka/4.2.7.64", res64) {
		t.Error("4.2.7.64 image with restricted_client=false must not trigger recreate")
	}
}

func TestGetWekaLocalResources_PreservesLargeIntegerAndUnknownField(t *testing.T) {
	// 2^63-1: unrepresentable exactly as float64, which is what a map[string]interface{}
	// round-trip would force it through.
	const hugeNumber = "9223372036854775807"
	r := &fakeRunner{scripted: map[string]string{
		"local resources -C backend0 --json": `{"huge_number":` + hugeNumber + `,"extra":{"nested":{"unknownField":"value"}}}`,
	}}

	resBytes, err := GetWekaLocalResources(context.Background(), r, "backend0")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseResourceDoc(resBytes)
	if err != nil {
		t.Fatal(err)
	}
	if perr := patchResourceDoc(&ContainerInput{}, false, doc, nil); perr != nil {
		t.Fatal(perr)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"huge_number":`+hugeNumber) {
		t.Errorf("large integer lost precision, got: %s", out)
	}
	if !strings.Contains(string(out), `"unknownField":"value"`) {
		t.Errorf("unknown nested field did not survive, got: %s", out)
	}
}

func TestPatchResourceDoc_DPDKExplicitZeroStaysZero(t *testing.T) {
	doc, err := ParseResourceDoc([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if perr := patchResourceDoc(&ContainerInput{DPDKBaseMiB: 0}, false, doc, nil); perr != nil {
		t.Fatal(perr)
	}
	b, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"dpdk_base_memory_mb":0`) {
		t.Errorf("explicit DPDKBaseMiB=0 must be written as 0, got: %s", b)
	}
}

func TestPatchResourceDoc_NvidiaVFSingleIP(t *testing.T) {
	base := []byte(`{}`)

	docNil, err := ParseResourceDoc(base)
	if err != nil {
		t.Fatal(err)
	}
	if perr := patchResourceDoc(&ContainerInput{}, false, docNil, nil); perr != nil {
		t.Fatal(perr)
	}
	b, err := docNil.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "nvidia_vf_single_ip") {
		t.Errorf("nvidia_vf_single_ip must be absent when input is nil, got: %s", b)
	}

	docSet, err := ParseResourceDoc(base)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if perr := patchResourceDoc(&ContainerInput{NvidiaVFSingleIP: &yes}, false, docSet, nil); perr != nil {
		t.Fatal(perr)
	}
	b2, err := docSet.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), `"nvidia_vf_single_ip":true`) {
		t.Errorf("nvidia_vf_single_ip must be present and true, got: %s", b2)
	}
}

func TestPatchResourceDoc_SurvivesUnknownFields(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "weka-resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseResourceDoc(raw)
	if err != nil {
		t.Fatal(err)
	}

	in := &ContainerInput{MemoryBytes: 1024, ManagementIPs: []string{"10.0.0.1"}}
	if perr := patchResourceDoc(in, false, doc, []string{"3", "4"}); perr != nil {
		t.Fatalf("patchResourceDoc on a doc missing nodes/net_devices must tolerate ErrFieldMissing: %v", perr)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	extra, ok := got["extra"].(map[string]interface{})
	if !ok {
		t.Fatalf("unknown top-level field %q did not survive patching: %s", "extra", out)
	}
	nested, ok := extra["nested"].(map[string]interface{})
	if !ok || nested["unknownField"] != "value" {
		t.Errorf("unknown nested field did not survive patching: %s", out)
	}
	if got["clusterGuid"] != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("unrelated existing field was clobbered: %s", out)
	}
	if got["memory"] != float64(1024) {
		t.Errorf("expected memory to be patched in, got: %s", out)
	}
}

func TestPatchResourceDoc_NodeCoreOrderNumeric(t *testing.T) {
	raw := []byte(`{"nodes":{"n0":{"roles":["FRONTEND"]},"n1":{"roles":["FRONTEND"]}}}`)
	doc, err := ParseResourceDoc(raw)
	if err != nil {
		t.Fatal(err)
	}

	fullCores := []string{"5", "7"}
	if perr := patchResourceDoc(&ContainerInput{}, false, doc, fullCores); perr != nil {
		t.Fatal(perr)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Nodes map[string]struct {
			CoreID int `json:"core_id"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Nodes["n0"].CoreID != 5 || got.Nodes["n1"].CoreID != 7 {
		t.Errorf("expected cores assigned in fullCores order (n0=5, n1=7), got: %s", out)
	}
}

func TestReconcileNetwork_RdmaOnlySelectorAddsDevice(t *testing.T) {
	r := &fakeRunner{scripted: map[string]string{
		"weka local resources -C client0 --json": `{"net_devices":[],"rdma_devices":{"devices":[]}}`,
	}}
	in := &ContainerInput{
		Runner: r,
		Name:   "client0",
		NetSelectors: []v1alpha1.NetworkSelector{
			{DeviceNames: []string{"eth1"}, RdmaOnly: true, Min: 1},
		},
	}

	if err := reconcileNetwork(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !r.ranAny("resources net -C client0 add eth1 --rdma-only") {
		t.Errorf("expected a rdma-only net add for eth1, calls: %+v", r.calls)
	}
}
