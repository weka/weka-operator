// Package osinfo provides OS/distro detection and hyperthreading detection for the pod runtime.
// It reads from host-side paths (/hostside/etc/os-release, /sys/...) and has no Kubernetes imports.
package osinfo

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

const (
	OsNameCos    = "cos"
	OsNameRhCos  = "rhcos"
	OsNameUbuntu = "ubuntu"
	// OsNameNixosPrefix prefixes NodeInfo.Os on NixOS, which is reported as "nixos-gcc<major>".
	OsNameNixosPrefix = "nixos"

	KubeDistroOpenshift = "openshift"
	KubeDistroGKE       = "gke"
	KubeDistroK8s       = "k8s"
)

// NodeInfo holds OS and distro information detected from the host filesystem.
type NodeInfo struct {
	Os               string
	OsBuildId        string
	KubernetesDistro string
}

func (n *NodeInfo) IsRhCos() bool  { return n.Os == OsNameRhCos }
func (n *NodeInfo) IsCos() bool    { return n.Os == OsNameCos }
func (n *NodeInfo) IsUbuntu() bool { return n.Os == OsNameUbuntu }
func (n *NodeInfo) IsNixos() bool  { return strings.HasPrefix(n.Os, OsNameNixosPrefix) }

// nixosHostPath is the PATH used to run host binaries on NixOS, where the host's /usr/bin has only
// `env` and everything else lives under /run/current-system/sw/bin.
const nixosHostPath = "/run/current-system/sw/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// HostNsenterArgs returns the nsenter arguments that run cmd in the host's mount and pid
// namespaces, routing through env with an explicit PATH on NixOS. A nil receiver is not NixOS.
func (n *NodeInfo) HostNsenterArgs(cmd ...string) []string {
	args := []string{"--mount", "--pid", "--target", "1", "--"}
	if n != nil && n.IsNixos() {
		args = append(args, "/usr/bin/env", "PATH="+nixosHostPath)
	}
	return append(args, cmd...)
}

// HostNsenterArgs is NodeInfo.HostNsenterArgs for the node this process runs on. When the OS
// cannot be determined the node is treated as a regular distro.
func HostNsenterArgs(cmd ...string) []string {
	n, err := Load()
	if err != nil {
		n = nil
	}
	return n.HostNsenterArgs(cmd...)
}

var (
	nodeInfoOnce   sync.Once
	nodeInfoCached *NodeInfo
	nodeInfoErr    error
)

// Load reads /hostside/etc/os-release and returns the detected NodeInfo.
// Results are cached after the first call; the OS does not change during a pod's lifetime.
func Load() (*NodeInfo, error) {
	nodeInfoOnce.Do(func() {
		nodeInfoCached, nodeInfoErr = load()
	})
	return nodeInfoCached, nodeInfoErr
}

func load() (*NodeInfo, error) {
	raw, err := parseOsRelease(osReleasePath)
	if err != nil {
		return nil, fmt.Errorf("reading os-release: %w", err)
	}

	info := &NodeInfo{
		Os:               raw["ID"],
		KubernetesDistro: KubeDistroK8s,
	}

	switch {
	case info.IsRhCos():
		info.KubernetesDistro = KubeDistroOpenshift
		info.OsBuildId = raw["VERSION"]
	case info.IsCos():
		info.KubernetesDistro = KubeDistroGKE
		info.OsBuildId = raw["BUILD_ID"]
	case info.IsUbuntu():
		info.OsBuildId = raw["VERSION_ID"]
	case info.IsNixos():
		// NixOS nodes are identified by the gcc that built the running kernel: drivers are built
		// against host headers with the builder image's toolchain, and a different gcc major
		// produces a module the kernel will not load.
		major, err := KernelGccMajor()
		if err != nil {
			return nil, err
		}
		info.Os = fmt.Sprintf("%s-gcc%s", OsNameNixosPrefix, major)
		info.OsBuildId = info.Os
	}

	return info, nil
}

// Paths are variables so tests can point them at fixtures.
var (
	osReleasePath   = "/hostside/etc/os-release"
	procVersionPath = "/proc/version"
)

var (
	gccParenRe   = regexp.MustCompile(`gcc \(GCC\) (\d+)\.`)
	gccVersionRe = regexp.MustCompile(`gcc version (\d+)\.`)
)

// ReadProcVersion returns the trimmed /proc/version contents. It reflects the host kernel in
// any container on the node, so no host mount is needed.
func ReadProcVersion() (string, error) {
	data, err := os.ReadFile(procVersionPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", procVersionPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// KernelGccMajor returns the major version of the gcc that built the running kernel.
func KernelGccMajor() (string, error) {
	procVersion, err := ReadProcVersion()
	if err != nil {
		return "", err
	}
	return ParseGccMajor(procVersion)
}

// ParseGccMajor extracts the gcc major version from /proc/version contents,
// e.g. "gcc (GCC) 15.2.0" -> "15" or "gcc version 14.2.0" -> "14".
func ParseGccMajor(procVersion string) (string, error) {
	if m := gccParenRe.FindStringSubmatch(procVersion); m != nil {
		return m[1], nil
	}
	if m := gccVersionRe.FindStringSubmatch(procVersion); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("could not parse gcc major version from %s: %s", procVersionPath, procVersion)
}

// IsHT returns true if CPU 0 has more than one thread sibling, indicating hyperthreading.
func IsHT() (bool, error) {
	path := "/sys/devices/system/cpu/cpu0/topology/thread_siblings_list"
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	siblings := parseThreadSiblingsList(strings.TrimSpace(string(data)))
	return len(siblings) > 1, nil
}

func parseOsRelease(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // close error on read-only file is not actionable

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		k := line[:idx]
		v := strings.Trim(line[idx+1:], `"`)
		if v != "" {
			result[k] = v
		}
	}
	return result, scanner.Err()
}

// parseThreadSiblingsList parses a comma/hyphen-separated list like "0-1" or "0,1".
func parseThreadSiblingsList(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			result = append(result, bounds[0], bounds[1])
		} else if part != "" {
			result = append(result, part)
		}
	}
	return result
}
