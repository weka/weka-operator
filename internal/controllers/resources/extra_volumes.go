package resources

import (
	"bytes"
	"encoding/json"
	"path"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ReservedVolumeNames are volume names the operator itself assigns somewhere in a weka pod
// (backend, client, or init container). Volumes are pod-scoped, so a name used only by an init
// container is reserved too: a collision would break that init container even though extras are
// never mounted into it (applyExtraVolumes mounts into the weka container only).
var ReservedVolumeNames = map[string]struct{}{
	"osrelease": {}, "dev": {}, "run": {}, "sys": {}, "weka-boot-scripts": {}, "hugepages": {}, "smbw-shm": {},
	"host-shared-netns": {}, "weka-container-persistence-dir": {}, "weka-container-shared-dir": {},
	"weka-cluster-persistence-dir": {}, "weka-container-global-persistence-dir": {},
	"weka-proxy-socket-dir": {}, "weka-ssdproxy-local-socket": {}, "node-info": {}, "weka-credentials": {},
	"proc-sysrq-trigger": {}, "proc-cmdline": {}, "devenv": {}, "google-cloud-key": {}, "host-modules": {},
	"host-usr-src": {}, "shared-weka-version": {}, "otel-packages": {}, "weka-pod-runtime-data": {},
	// The drivers container declares these alongside, not instead of, the host-* pair above;
	// all are live volume names, so all are reserved (see resources/drivers.go).
	"libmodules": {}, "usrsrc": {}, "gcloud-credentials": {},
	// AdditionalSecrets has exactly one entry today and it becomes "<name>-secret" (pod.go).
	// Reserve that literal derived name rather than banning every "-secret" suffix, which would
	// also reject plausible user volume names like "corp-ca-secret".
	"wekahome-cacert-secret": {},
}

// ReservedMountPaths are mount paths the operator manages. A path is reserved when it equals an
// entry, falls under one at a /-boundary, or sits above one (see IsReservedMountPath), so an
// entry naming a file reserves only itself and its ancestors.
// /etc/ssl and /etc/pki are deliberately absent: mounting a CA bundle there is the motivating
// use case.
var ReservedMountPaths = []string{
	"/dev", "/sys", "/host", "/host-binds", "/hostside", "/opt/weka",
	"/opt/weka-global-persistence", "/var/run/secrets/weka-operator", "/usr/local/bin/weka",
	"/etc/wekaio", "/etc/syslog-ng", "/shared-python-packages", "/shared-weka-version", "/weka-pod-runtime-data",
	"/var/log", "/lib/modules", "/usr/src", "/var/secrets/google",
	// Files, not directories: the /-boundary rule means "/opt/weka" does not cover a sibling
	// like "/opt/weka_runtime.py", so each of these needs its own entry.
	"/opt/weka_runtime.py",       // pod.go: weka_runtime.py mount
	"/usr/local/bin/wekaauthcli", // pod.go: wekaauthcli mount
	"/usr/bin/weka",              // init_containers.go: drivers-loader CLI shadow mount
	"/devenv.sh",                 // drivers.go: COS dev-env script mount
}

// IsReservedVolumeName reports whether name collides with an operator-managed volume.
func IsReservedVolumeName(name string) bool {
	_, ok := ReservedVolumeNames[name]
	return ok
}

// IsReservedMountPath reports whether p is, falls under, or sits above an operator-managed mount
// path. Extra mounts are appended after the operator's own, so a mount at an ancestor (/, /opt)
// would shadow the reserved path just as surely as a mount on it.
func IsReservedMountPath(p string) bool {
	clean := path.Clean(p)
	if clean == "/" {
		return true
	}
	for _, reserved := range ReservedMountPaths {
		if clean == reserved || strings.HasPrefix(clean, reserved+"/") || strings.HasPrefix(reserved, clean+"/") {
			return true
		}
	}
	return false
}

// NormalizeExtraVolumes round-trips raw through []corev1.Volume and re-marshals it to canonical
// JSON, so key order and whitespace differences compare equal; unset, null, and an empty array
// all collapse to nil. Decoding is strict (unlike the lenient GetExtraVolumes), matching
// admission's own validator, so a decode failure returns the raw bytes unchanged rather than nil -
// malformed input must still reach the pod-build path's own error instead of silently vanishing.
func NormalizeExtraVolumes(raw *runtime.RawExtension) *runtime.RawExtension {
	if raw == nil || len(raw.Raw) == 0 {
		return nil
	}
	var volumes []corev1.Volume
	dec := json.NewDecoder(bytes.NewReader(raw.Raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&volumes); err != nil {
		return &runtime.RawExtension{Raw: bytes.Clone(raw.Raw)}
	}
	if len(volumes) == 0 {
		return nil
	}
	normalized, _ := json.Marshal(volumes) //nolint:errcheck // volumes just decoded from JSON
	return &runtime.RawExtension{Raw: normalized}
}

// ExtraVolumesEqual reports whether a and b are the same normalized extraVolumes bytes, treating
// nil and an empty RawExtension as equal.
func ExtraVolumesEqual(a, b *runtime.RawExtension) bool {
	var araw, braw []byte
	if a != nil {
		araw = a.Raw
	}
	if b != nil {
		braw = b.Raw
	}
	return bytes.Equal(araw, braw)
}

// ExtraVolumeMountsEqual compares mounts by value, treating nil and empty as equal so an
// unset spec never looks like a change. reflect.DeepEqual rather than slices.Equal: VolumeMount
// has pointer fields, which == compares by address, and these are freshly unmarshaled each
// reconcile.
func ExtraVolumeMountsEqual(a, b []corev1.VolumeMount) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
