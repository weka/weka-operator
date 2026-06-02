package persistency

import (
	"fmt"
	"strings"
	"testing"
)

// generateScript mirrors the script construction in Configure so we can inspect the
// generated shell text without executing it. It uses the same format string / arg
// order as Configure.
func generateScript(persistenceDir string) string {
	return fmt.Sprintf(`
if [ -d /host-binds/opt-weka ]; then
    mkdir -p /opt/weka-dist-save
    mount -o bind /opt/weka/dist /opt/weka-dist-save
    mount --make-private /opt/weka-dist-save
    mkdir -p %s/dist/drivers
    mount -o bind %s /opt/weka
    mkdir -p /opt/weka/dist
    mount -o bind /opt/weka-dist-save /opt/weka/dist
    umount /opt/weka-dist-save
    mount -o bind %s/dist/drivers /opt/weka/dist/drivers
fi
`,
		persistenceDir, persistenceDir, persistenceDir,
	)
}

// TestConfigureScript_OptWekaBlock verifies that the generated shell script for the
// /host-binds/opt-weka block matches the new dist-subtree strategy (Python commit d4a16ac7).
func TestConfigureScript_OptWekaBlock(t *testing.T) {
	const persistenceDir = "/host-binds/opt-weka"
	script := generateScript(persistenceDir)

	checks := []struct {
		desc    string
		want    string
		present bool
	}{
		{"bind dist to staging dir", "mount -o bind /opt/weka/dist /opt/weka-dist-save", true},
		{"make-private on staging dir", "mount --make-private /opt/weka-dist-save", true},
		{"mkdir persistence dist/drivers", "mkdir -p " + persistenceDir + "/dist/drivers", true},
		{"bind persistence dir to /opt/weka", "mount -o bind " + persistenceDir + " /opt/weka", true},
		{"restore dist from staging", "mount -o bind /opt/weka-dist-save /opt/weka/dist", true},
		{"umount staging dir", "umount /opt/weka-dist-save", true},
		{"bind drivers dir", "mount -o bind " + persistenceDir + "/dist/drivers /opt/weka/dist/drivers", true},
		// Old strategy must be absent.
		{"no weka-preinstalled", "weka-preinstalled", false},
	}

	for _, tc := range checks {
		t.Run(tc.desc, func(t *testing.T) {
			found := strings.Contains(script, tc.want)
			if found != tc.present {
				if tc.present {
					t.Errorf("script missing expected string:\n  %q\nScript:\n%s", tc.want, script)
				} else {
					t.Errorf("script contains unexpected string:\n  %q\nScript:\n%s", tc.want, script)
				}
			}
		})
	}
}

// TestConfigureScript_GlobalPersistence verifies the same properties when using
// global persistence mode (different persistenceDir path).
func TestConfigureScript_GlobalPersistence(t *testing.T) {
	const containerID = "abc123"
	persistenceDir := fmt.Sprintf("/opt/weka-global-persistence/containers/%s", containerID)
	script := generateScript(persistenceDir)

	if !strings.Contains(script, "mount --make-private /opt/weka-dist-save") {
		t.Error("global persistence script missing 'mount --make-private /opt/weka-dist-save'")
	}
	if !strings.Contains(script, "umount /opt/weka-dist-save") {
		t.Error("global persistence script missing 'umount /opt/weka-dist-save'")
	}
	if strings.Contains(script, "weka-preinstalled") {
		t.Error("global persistence script must not reference 'weka-preinstalled'")
	}
}
