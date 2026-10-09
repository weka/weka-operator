// Package weka — resourcedoc.go implements ResourceDoc, a document-preserving representation
// of a Weka-owned resource document (weka-resources.<id>.json / resources.json), plus the
// accessors and mutations the runtime needs. Field names/shapes mirror ensure.go, ssdproxy.go,
// and weka_runtime.py's resources dict edits.
package weka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/google/uuid"
)

// resourceLinkNames are the symlinks a versioned weka-resources.*.json file is relinked to.
var resourceLinkNames = []string{"resources.json", "resources.json.stable", "resources.json.staging"}

// ErrFieldMissing indicates a resource-document field was absent.
var ErrFieldMissing = errors.New("field missing")

// ErrFieldType indicates a resource-document field had an unexpected JSON type.
var ErrFieldType = errors.New("unexpected field type")

// Endpoint is one backend endpoint in the resource document.
type Endpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// ResourceDoc is a Weka-owned resource document that preserves unknown fields and numeric
// precision by holding every value as json.RawMessage.
type ResourceDoc struct {
	fields map[string]json.RawMessage
	order  []string
}

// ParseResourceDoc parses a resource document, preserving top-level key order and raw values.
func ParseResourceDoc(b []byte) (*ResourceDoc, error) {
	fields, order, err := decodeOrderedObject(b)
	if err != nil {
		return nil, fmt.Errorf("parse resource doc: %w", err)
	}
	return &ResourceDoc{fields: fields, order: order}, nil
}

// Bytes re-encodes the document: keys in original order, then any newly added keys.
func (d *ResourceDoc) Bytes() ([]byte, error) {
	return encodeOrderedObject(d.fields, d.order)
}

// decodeOrderedObject decodes a JSON object into its raw per-key values plus the order keys
// first appeared in. Values are kept as json.RawMessage so untouched numbers, nested objects,
// and arrays round-trip byte-for-byte.
func decodeOrderedObject(b []byte) (fields map[string]json.RawMessage, order []string, err error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var tok json.Token
	tok, err = dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("expected JSON object, got %v", tok)
	}
	fields = map[string]json.RawMessage{}
	for dec.More() {
		var keyTok json.Token
		keyTok, err = dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected string key, got %v", keyTok)
		}
		var raw json.RawMessage
		if decErr := dec.Decode(&raw); decErr != nil {
			return nil, nil, decErr
		}
		if _, exists := fields[key]; !exists {
			order = append(order, key)
		}
		fields[key] = raw
	}
	if _, err = dec.Token(); err != nil { // closing '}'
		return nil, nil, err
	}
	return fields, order, nil
}

// encodeOrderedObject is the inverse of decodeOrderedObject.
func encodeOrderedObject(fields map[string]json.RawMessage, order []string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	seen := make(map[string]bool, len(fields))
	first := true
	write := func(key string) error {
		val, ok := fields[key]
		if !ok || seen[key] {
			return nil
		}
		seen[key] = true
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(val)
		return nil
	}
	for _, key := range order {
		if err := write(key); err != nil {
			return nil, err
		}
	}
	extra := make([]string, 0, len(fields)-len(seen))
	for key := range fields {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		if err := write(key); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// field returns the raw value for name, or ErrFieldMissing wrapped with the field name.
func (d *ResourceDoc) field(name string) (json.RawMessage, error) {
	v, ok := d.fields[name]
	if !ok {
		return nil, fmt.Errorf("%s: %w", name, ErrFieldMissing)
	}
	return v, nil
}

// set marshals v and stores it under key, appending key to order if new.
func (d *ResourceDoc) set(key string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		// All setter inputs are well-formed Go values; Marshal cannot fail for them.
		panic(fmt.Sprintf("resourcedoc: marshal %s: %v", key, err))
	}
	d.setRaw(key, b)
}

func (d *ResourceDoc) setRaw(key string, raw json.RawMessage) {
	if _, exists := d.fields[key]; !exists {
		d.order = append(d.order, key)
	}
	d.fields[key] = raw
}

// nodesObj decodes the "nodes" object, preserving per-node raw bytes and key order.
func (d *ResourceDoc) nodesObj() (fields map[string]json.RawMessage, order []string, err error) {
	raw, err := d.field("nodes")
	if err != nil {
		return nil, nil, err
	}
	fields, order, err = decodeOrderedObject(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("nodes: %w", ErrFieldType)
	}
	return fields, order, nil
}

// NodeIDs returns node IDs in ascending numeric order (falling back to lexical order for
// non-numeric IDs).
func (d *ResourceDoc) NodeIDs() ([]string, error) {
	fields, _, err := d.nodesObj()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(fields))
	for id := range fields {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ni, iErr := strconv.Atoi(ids[i])
		nj, jErr := strconv.Atoi(ids[j])
		if iErr == nil && jErr == nil {
			return ni < nj
		}
		return ids[i] < ids[j]
	})
	return ids, nil
}

// NodeRoles returns the "roles" field of node id.
func (d *ResourceDoc) NodeRoles(id string) ([]string, error) {
	nodes, _, err := d.nodesObj()
	if err != nil {
		return nil, err
	}
	nodeRaw, ok := nodes[id]
	if !ok {
		return nil, fmt.Errorf("nodes.%s: %w", id, ErrFieldMissing)
	}
	nodeFields, _, err := decodeOrderedObject(nodeRaw)
	if err != nil {
		return nil, fmt.Errorf("nodes.%s: %w", id, ErrFieldType)
	}
	rolesRaw, ok := nodeFields["roles"]
	if !ok {
		return nil, fmt.Errorf("nodes.%s.roles: %w", id, ErrFieldMissing)
	}
	var roles []string
	if err := json.Unmarshal(rolesRaw, &roles); err != nil {
		return nil, fmt.Errorf("nodes.%s.roles: %w", id, ErrFieldType)
	}
	return roles, nil
}

// netDevices decodes the "net_devices" array, preserving each element's raw bytes.
func (d *ResourceDoc) netDevices() ([]json.RawMessage, error) {
	raw, err := d.field("net_devices")
	if err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("net_devices: %w", ErrFieldType)
	}
	return arr, nil
}

// NetDeviceCount returns the number of entries in "net_devices".
func (d *ResourceDoc) NetDeviceCount() (int, error) {
	arr, err := d.netDevices()
	if err != nil {
		return 0, err
	}
	return len(arr), nil
}

func (d *ResourceDoc) SetAllowProtocols(v bool)         { d.set("allow_protocols", v) }
func (d *ResourceDoc) SetReserve1GHugepages(v bool)     { d.set("reserve_1g_hugepages", v) }
func (d *ResourceDoc) SetExcludedDrivers(v []string)    { d.set("excluded_drivers", v) }
func (d *ResourceDoc) SetMemory(bytesVal int64)         { d.set("memory", bytesVal) }
func (d *ResourceDoc) SetAutoDiscoveryEnabled(v bool)   { d.set("auto_discovery_enabled", v) }
func (d *ResourceDoc) SetIPs(v []string)                { d.set("ips", v) }
func (d *ResourceDoc) SetDPDKBaseMemoryMB(v int)        { d.set("dpdk_base_memory_mb", v) }
func (d *ResourceDoc) SetNonDatapathCores(v []int)      { d.set("non_datapath_cores", v) }
func (d *ResourceDoc) SetAutoRemoveTimeout(v int)       { d.set("auto_remove_timeout", v) }
func (d *ResourceDoc) SetBackendEndpoints(v []Endpoint) { d.set("backend_endpoints", v) }
func (d *ResourceDoc) SetRestrictListen(v bool)         { d.set("restrict_listen", v) }

// SetNvidiaVFSingleIP sets "nvidia_vf_single_ip". Callers must skip calling this when the
// underlying config value is unset (nil) — Python/weka_runtime.py only writes the field when
// NVIDIA_VF_SINGLE_IP is set in the environment; writing false unconditionally would diverge.
func (d *ResourceDoc) SetNvidiaVFSingleIP(v bool) { d.set("nvidia_vf_single_ip", v) }

// setNetDeviceField patches one key on net_devices[idx], preserving that element's other
// fields/order and every other element's raw bytes.
func (d *ResourceDoc) setNetDeviceField(idx int, key string, value interface{}) error {
	arr, err := d.netDevices()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(arr) {
		return fmt.Errorf("net_devices[%d]: %w", idx, ErrFieldMissing)
	}
	fields, order, err := decodeOrderedObject(arr[idx])
	if err != nil {
		return fmt.Errorf("net_devices[%d]: %w", idx, ErrFieldType)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, exists := fields[key]; !exists {
		order = append(order, key)
	}
	fields[key] = b
	encoded, err := encodeOrderedObject(fields, order)
	if err != nil {
		return err
	}
	arr[idx] = encoded
	full, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	d.setRaw("net_devices", full)
	return nil
}

// SetNetDeviceGateway sets net_devices[idx].gateway. ensure.go only ever calls this for idx==0,
// erroring first when net_devices doesn't have exactly one element.
func (d *ResourceDoc) SetNetDeviceGateway(idx int, gw string) error {
	return d.setNetDeviceField(idx, "gateway", gw)
}

// SetNetDeviceNetmask sets net_devices[idx].netmask.
func (d *ResourceDoc) SetNetDeviceNetmask(idx, mask int) error {
	return d.setNetDeviceField(idx, "netmask", mask)
}

// SetNodeCore sets nodes[id].core_id and dedicate_core, matching ensure.go's per-node core
// assignment. dedicatedMode is written as nodes[id].dedicated_mode only when non-empty: ensure.go
// only sets that key (to "NONE") under the "shared" CPU policy, and never writes it otherwise —
// an empty dedicatedMode reproduces "never writes it".
func (d *ResourceDoc) SetNodeCore(id string, coreID int, dedicate bool, dedicatedMode string) error {
	fields, order, err := d.nodesObj()
	if err != nil {
		return err
	}
	nodeRaw, ok := fields[id]
	if !ok {
		return fmt.Errorf("nodes.%s: %w", id, ErrFieldMissing)
	}
	nodeFields, nodeOrder, err := decodeOrderedObject(nodeRaw)
	if err != nil {
		return fmt.Errorf("nodes.%s: %w", id, ErrFieldType)
	}
	setField := func(key string, v interface{}) error {
		b, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return marshalErr
		}
		if _, exists := nodeFields[key]; !exists {
			nodeOrder = append(nodeOrder, key)
		}
		nodeFields[key] = b
		return nil
	}
	if setErr := setField("dedicate_core", dedicate); setErr != nil {
		return setErr
	}
	if dedicatedMode != "" {
		if setErr := setField("dedicated_mode", dedicatedMode); setErr != nil {
			return setErr
		}
	}
	if setErr := setField("core_id", coreID); setErr != nil {
		return setErr
	}
	encodedNode, err := encodeOrderedObject(nodeFields, nodeOrder)
	if err != nil {
		return err
	}
	if _, exists := fields[id]; !exists {
		order = append(order, id)
	}
	fields[id] = encodedNode
	encodedNodes, err := encodeOrderedObject(fields, order)
	if err != nil {
		return err
	}
	d.setRaw("nodes", encodedNodes)
	return nil
}

// relink atomically points dir/linkName at target (a filename relative to dir), replicating
// `ln -sf target dir/linkName` without shelling out.
func relink(dir, target, linkName string) error {
	linkPath := filepath.Join(dir, linkName)
	tmp := linkPath + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale %s: %w", tmp, err)
	}
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("symlink %s: %w", linkName, err)
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		return fmt.Errorf("rename %s: %w", linkName, err)
	}
	return nil
}

// WriteResourceDoc stages d under a fresh versioned filename (weka-resources.<uuid>.json,
// matching Python's uuid.uuid4()-based generation and ssdproxy.go) and relinks resources.json,
// resources.json.stable, and resources.json.staging to it.
func WriteResourceDoc(_ context.Context, dir string, d *ResourceDoc) error {
	data, err := d.Bytes()
	if err != nil {
		return fmt.Errorf("write resource doc: %w", err)
	}
	fileName := fmt.Sprintf("weka-resources.%s.json", uuid.NewString())
	if err := os.WriteFile(filepath.Join(dir, fileName), data, 0o644); err != nil {
		return fmt.Errorf("write resource doc: %w", err)
	}
	for _, link := range resourceLinkNames {
		if err := relink(dir, fileName, link); err != nil {
			return fmt.Errorf("write resource doc: %w", err)
		}
	}
	return nil
}
