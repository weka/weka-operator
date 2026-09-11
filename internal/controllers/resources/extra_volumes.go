package resources

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/weka/weka-operator/pkg/util"
)

// ReservedVolumeNames are volume names the operator itself assigns somewhere in a weka pod
// (backend, client, or init container). Volumes are pod-scoped, so a name used only by an init
// container is reserved too: a collision would break that init container even though extras are
// never mounted into it (applyExtraVolumes mounts into the weka container only).
var ReservedVolumeNames = []string{
	"osrelease", "dev", "run", "sys", "weka-boot-scripts", "hugepages", "smbw-shm",
	"host-shared-netns", "weka-container-persistence-dir", "weka-container-shared-dir",
	"weka-cluster-persistence-dir", "weka-container-global-persistence-dir",
	"weka-proxy-socket-dir", "weka-ssdproxy-local-socket", "node-info", "weka-credentials",
	"proc-sysrq-trigger", "proc-cmdline", "devenv", "google-cloud-key", "host-modules",
	"host-usr-src", "shared-weka-version", "otel-packages",
	// The drivers container declares these alongside, not instead of, the host-* pair above;
	// all are live volume names, so all are reserved (see resources/drivers.go).
	"libmodules", "usrsrc", "gcloud-credentials",
	// AdditionalSecrets has exactly one entry today and it becomes "<name>-secret" (pod.go).
	// Reserve that literal derived name rather than banning every "-secret" suffix, which would
	// also reject plausible user volume names like "corp-ca-secret".
	"wekahome-cacert-secret",
}

// ReservedMountPaths are mount paths the operator manages. A path is reserved when it equals an
// entry or falls under one at a /-boundary, so an entry naming a file reserves only itself.
// /etc/ssl and /etc/pki are deliberately absent: mounting a CA bundle there is the motivating
// use case.
var ReservedMountPaths = []string{
	"/dev", "/sys", "/host", "/host-binds", "/hostside", "/opt/weka",
	"/opt/weka-global-persistence", "/var/run/secrets/weka-operator", "/usr/local/bin/weka",
	"/etc/wekaio", "/etc/syslog-ng", "/shared-python-packages", "/shared-weka-version",
	"/var/log", "/lib/modules", "/usr/src", "/var/secrets/google",
	// Files, not directories: the /-boundary rule means "/opt/weka" does not cover a sibling
	// like "/opt/weka_runtime.py", so each of these needs its own entry.
	"/opt/weka_runtime.py",       // pod.go: weka_runtime.py mount
	"/usr/local/bin/wekaauthcli", // pod.go: wekaauthcli mount
	"/usr/bin/weka",              // init_containers.go: drivers-loader CLI shadow mount
	"/devenv.sh",                 // drivers.go: COS dev-env script mount
}

var reservedVolumeNameSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(ReservedVolumeNames))
	for _, name := range ReservedVolumeNames {
		set[name] = struct{}{}
	}
	return set
}()

// IsReservedVolumeName reports whether name collides with an operator-managed volume.
func IsReservedVolumeName(name string) bool {
	_, ok := reservedVolumeNameSet[name]
	return ok
}

// IsReservedMountPath reports whether p is, or falls under, an operator-managed mount path.
func IsReservedMountPath(p string) bool {
	clean := path.Clean(p)
	for _, reserved := range ReservedMountPaths {
		if clean == reserved || strings.HasPrefix(clean, reserved+"/") {
			return true
		}
	}
	return false
}

// NormalizeExtraVolumes round-trips raw through []corev1.Volume and re-marshals it to canonical
// JSON, so key order and whitespace differences hash identically. Unset, JSON null, and an empty
// array all collapse to nil so none of the three churns the spec digest against the others.
//
// Decoding is strict, matching admission's own validator: the CRD field is PreserveUnknownFields,
// so a lenient decode would silently drop a field the vendored corev1.Volume does not know about -
// permanently, since the normalized form is what gets written onto the WekaContainer spec. On any
// decode failure the raw bytes are copied through unchanged rather than collapsing to nil, so
// malformed input still reaches the pod-build path and fails loudly in applyExtraVolumes instead
// of vanishing before it gets there.
//
// The returned RawExtension always wraps freshly allocated bytes, never the caller's own slice,
// because normalized specs get written back to the API server.
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

// ExtraVolumesDigest hashes the normalized form of raw. JSON, unlike the gob encoding behind
// util.HashStruct, sorts map keys and ignores none of a Volume's fields, so a change buried in
// e.g. emptyDir.sizeLimit (a resource.Quantity, whose value lives in unexported fields gob
// skips) or csi.volumeAttributes (a map, which gob refuses outright) still changes the digest.
func ExtraVolumesDigest(raw *runtime.RawExtension) string {
	normalized := NormalizeExtraVolumes(raw)
	if normalized == nil {
		return ""
	}
	return util.GetHash(string(normalized.Raw), 16)
}

// ExtraVolumeMountsDigest hashes mounts the same way ExtraVolumesDigest hashes volumes.
func ExtraVolumeMountsDigest(mounts []corev1.VolumeMount) string {
	if len(mounts) == 0 {
		return ""
	}
	data, err := json.Marshal(mounts)
	if err != nil {
		return ""
	}
	return util.GetHash(string(data), 16)
}
