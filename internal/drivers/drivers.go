package drivers

import (
	"fmt"
	"regexp"
	"strings"
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
