package weka

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const testdataResourceDoc = "../testdata/weka-resources.json"

func TestResourceDocPreservesUnknownFieldsAndPrecision(t *testing.T) {
	orig, err := os.ReadFile(testdataResourceDoc)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseResourceDoc(orig)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate an unrelated field; untouched nested structures must survive byte-for-byte.
	d.SetReserve1GHugepages(false)

	out, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	reparsed, err := ParseResourceDoc(out)
	if err != nil {
		t.Fatal(err)
	}

	if string(reparsed.fields["generation"]) != "9007199254740993" {
		t.Fatalf("generation lost precision: got %s", reparsed.fields["generation"])
	}
	if string(reparsed.fields["extra"]) != string(mustField(t, orig, "extra")) {
		t.Fatalf("unknown nested field 'extra' not preserved byte-for-byte:\ngot:  %s\nwant: %s",
			reparsed.fields["extra"], mustField(t, orig, "extra"))
	}
	if string(reparsed.fields["clusterGuid"]) != string(mustField(t, orig, "clusterGuid")) {
		t.Fatalf("unrelated field 'clusterGuid' not preserved")
	}
}

// mustField decodes b as an ordered object and returns the raw value for key.
func mustField(t *testing.T, b []byte, key string) []byte {
	t.Helper()
	fields, _, err := decodeOrderedObject(b)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := fields[key]
	if !ok {
		t.Fatalf("fixture missing field %q", key)
	}
	return v
}

func TestResourceDocMissingVsWrongType(t *testing.T) {
	d, err := ParseResourceDoc([]byte(`{"net_devices": "not-an-array"}`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.field("missing_field"); !errors.Is(err, ErrFieldMissing) {
		t.Fatalf("expected ErrFieldMissing, got %v", err)
	}

	if _, err := d.NetDeviceCount(); !errors.Is(err, ErrFieldType) {
		t.Fatalf("expected ErrFieldType for net_devices, got %v", err)
	}
}

func TestResourceDocNodeIDsAscendingNumeric(t *testing.T) {
	d, err := ParseResourceDoc([]byte(`{"nodes": {"9": {"roles": ["COMPUTE"]}, "10": {"roles": ["COMPUTE"]}, "2": {"roles": ["COMPUTE"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := d.NodeIDs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2", "9", "10"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
}

func TestResourceDocNodeRoles(t *testing.T) {
	d, err := ParseResourceDoc([]byte(`{"nodes": {"0": {"roles": ["MANAGEMENT", "COMPUTE"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	roles, err := d.NodeRoles("0")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || roles[0] != "MANAGEMENT" || roles[1] != "COMPUTE" {
		t.Fatalf("got %v", roles)
	}
	if _, err := d.NodeRoles("nope"); !errors.Is(err, ErrFieldMissing) {
		t.Fatalf("expected ErrFieldMissing, got %v", err)
	}
}

func TestResourceDocSetters(t *testing.T) {
	base := `{"nodes": {"0": {"roles": ["MANAGEMENT"], "extra": "keep"}}, "net_devices": [{"name": "eth0", "extra": "keep"}]}`
	d, err := ParseResourceDoc([]byte(base))
	if err != nil {
		t.Fatal(err)
	}

	d.SetAllowProtocols(true)
	d.SetReserve1GHugepages(false)
	d.SetExcludedDrivers([]string{"igb_uio"})
	d.SetMemory(1073741824)
	d.SetAutoDiscoveryEnabled(false)
	d.SetIPs([]string{"10.0.0.1"})
	d.SetDPDKBaseMemoryMB(64)
	d.SetNonDatapathCores([]int{1, 2})
	d.SetAutoRemoveTimeout(300)
	d.SetBackendEndpoints([]Endpoint{{IP: "10.0.0.2", Port: 14000}})
	d.SetRestrictListen(true)
	d.SetNvidiaVFSingleIP(true)

	if err = d.SetNetDeviceGateway(0, "10.0.0.254"); err != nil {
		t.Fatal(err)
	}
	if err = d.SetNetDeviceNetmask(0, 24); err != nil {
		t.Fatal(err)
	}
	if err = d.SetNodeCore("0", 5, true, ""); err != nil {
		t.Fatal(err)
	}

	out, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	fields, _, err := decodeOrderedObject(out)
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		"allow_protocols":        "true",
		"reserve_1g_hugepages":   "false",
		"excluded_drivers":       `["igb_uio"]`,
		"memory":                 "1073741824",
		"auto_discovery_enabled": "false",
		"ips":                    `["10.0.0.1"]`,
		"dpdk_base_memory_mb":    "64",
		"non_datapath_cores":     "[1,2]",
		"auto_remove_timeout":    "300",
		"backend_endpoints":      `[{"ip":"10.0.0.2","port":14000}]`,
		"restrict_listen":        "true",
		"nvidia_vf_single_ip":    "true",
	}
	for key, want := range checks {
		if got := string(fields[key]); got != want {
			t.Errorf("field %q: got %s, want %s", key, got, want)
		}
	}

	netDevs, err := d.netDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(netDevs) != 1 {
		t.Fatalf("expected 1 net device, got %d", len(netDevs))
	}
	netFields, _, err := decodeOrderedObject(netDevs[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(netFields["gateway"]) != `"10.0.0.254"` {
		t.Errorf("gateway: got %s", netFields["gateway"])
	}
	if string(netFields["netmask"]) != "24" {
		t.Errorf("netmask: got %s", netFields["netmask"])
	}
	if string(netFields["name"]) != `"eth0"` {
		t.Errorf("net device unrelated field 'name' lost: got %s", netFields["name"])
	}
	if string(netFields["extra"]) != `"keep"` {
		t.Errorf("net device unrelated field 'extra' lost: got %s", netFields["extra"])
	}

	nodesFields, _, err := decodeOrderedObject(fields["nodes"])
	if err != nil {
		t.Fatal(err)
	}
	nodeFields, _, err := decodeOrderedObject(nodesFields["0"])
	if err != nil {
		t.Fatal(err)
	}
	if string(nodeFields["core_id"]) != "5" {
		t.Errorf("core_id: got %s", nodeFields["core_id"])
	}
	if string(nodeFields["dedicate_core"]) != "true" {
		t.Errorf("dedicate_core: got %s", nodeFields["dedicate_core"])
	}
	if _, ok := nodeFields["dedicated_mode"]; ok {
		t.Errorf("dedicated_mode should not be written when dedicatedMode is empty, got %s", nodeFields["dedicated_mode"])
	}
	if string(nodeFields["extra"]) != `"keep"` {
		t.Errorf("node unrelated field 'extra' lost: got %s", nodeFields["extra"])
	}
	if string(nodeFields["roles"]) != `["MANAGEMENT"]` {
		t.Errorf("node roles lost: got %s", nodeFields["roles"])
	}
}

func TestResourceDocSetNodeCoreDedicatedMode(t *testing.T) {
	d, err := ParseResourceDoc([]byte(`{"nodes": {"0": {"roles": ["COMPUTE"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = d.SetNodeCore("0", 3, false, "NONE"); err != nil {
		t.Fatal(err)
	}
	nodesFields, _, err := decodeOrderedObject(d.fields["nodes"])
	if err != nil {
		t.Fatal(err)
	}
	nodeFields, _, err := decodeOrderedObject(nodesFields["0"])
	if err != nil {
		t.Fatal(err)
	}
	if string(nodeFields["dedicated_mode"]) != `"NONE"` {
		t.Errorf("dedicated_mode: got %s", nodeFields["dedicated_mode"])
	}
	if string(nodeFields["dedicate_core"]) != "false" {
		t.Errorf("dedicate_core: got %s", nodeFields["dedicate_core"])
	}
}

func TestResourceDocSetNetDeviceGatewayMissingIndex(t *testing.T) {
	d, err := ParseResourceDoc([]byte(`{"net_devices": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetNetDeviceGateway(0, "10.0.0.1"); !errors.Is(err, ErrFieldMissing) {
		t.Fatalf("expected ErrFieldMissing, got %v", err)
	}
}

func TestWriteResourceDoc(t *testing.T) {
	dir := t.TempDir()
	d, err := ParseResourceDoc([]byte(`{"base_port": 14000}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteResourceDoc(nil, dir, d); err != nil { //nolint:staticcheck // nil context acceptable in test; body ignores it
		t.Fatal(err)
	}

	for _, link := range []string{"resources.json", "resources.json.stable", "resources.json.staging"} {
		target, err := os.Readlink(filepath.Join(dir, link))
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if !filepathMatchesVersionedName(target) {
			t.Errorf("%s points at unexpected target %q", link, target)
		}
		data, err := os.ReadFile(filepath.Join(dir, link))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"base_port":14000}` {
			t.Errorf("%s content: got %s", link, data)
		}
	}
}

func filepathMatchesVersionedName(name string) bool {
	matched, _ := filepath.Match("weka-resources.*.json", name)
	return matched
}
