// Package osinfo provides OS/distro detection and hyperthreading detection for the pod runtime.
// It reads from host-side paths (/hostside/etc/os-release, /sys/...) and has no Kubernetes imports.
package osinfo

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const (
	OsNameCos    = "cos"
	OsNameRhCos  = "rhcos"
	OsNameUbuntu = "ubuntu"

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

// Load reads /hostside/etc/os-release and returns the detected NodeInfo.
// This is the host-side path mounted into the pod.
func Load() (*NodeInfo, error) {
	raw, err := parseOsRelease("/hostside/etc/os-release")
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
	}

	return info, nil
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
