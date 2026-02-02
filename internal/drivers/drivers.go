package drivers

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/weka/weka-operator/internal/services"
	v1 "k8s.io/api/core/v1"
)

var (
	ubuntuRe = regexp.MustCompile(`(?i)ubuntu\s+(\d+)\.(\d+)`)
	rhelRe   = regexp.MustCompile(`(?i)rhel\s*(\d+)`)
	rockyRe  = regexp.MustCompile(`(?i)rocky\s*(\d+)`)
)

// NormalizeOSImageName converts OS image names into short canonical IDs.
// Examples:
//
//	"Ubuntu 22.04.5 LTS" -> "ubuntu-22.04"
//	"Ubuntu 24.04.3 LTS" -> "ubuntu-24"
//	"RHEL 9.4"           -> "rhel09"
//	"Rocky Linux 8.10"   -> "rocky08"
func NormalizeOSImageName(input string) string {
	s := strings.TrimSpace(input)

	// Ubuntu
	if m := ubuntuRe.FindStringSubmatch(s); m != nil {
		major := m[1]
		minor := m[2]

		// Special rule: 24.04 → ubuntu-24
		if major == "24" && minor == "04" {
			return "ubuntu-24"
		}

		return fmt.Sprintf("ubuntu-%s.%s", major, minor)
	}

	// RHEL
	if m := rhelRe.FindStringSubmatch(s); m != nil {
		return fmt.Sprintf("rhel%02s", m[1])
	}

	// Rocky
	if m := rockyRe.FindStringSubmatch(s); m != nil {
		return fmt.Sprintf("rocky%02s", m[1])
	}

	return "unknown-os"
}

func GetBuilderImageForNode(node *v1.Node) string {
	osImage := node.Status.NodeInfo.OSImage
	switch {
	case strings.Contains(osImage, "Ubuntu 24.04"):
		return "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"
	default:
		return "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"

	}
}

func GetLoaderImageForNode(ctx context.Context, node *v1.Node, image string) string {
	flags, err := services.GetFeatureFlags(ctx, image)
	if err == nil && flags != nil {
		// innovation cli --kernel-build-id etc.
		if flags.WekaGetCopyLocalDriverFiles {
			return image
		}
	}

	// else - can use the builder image that has "innovation" cli
	return GetBuilderImageForNode(node)
}
