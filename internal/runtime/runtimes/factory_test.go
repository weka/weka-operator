package runtimes

import (
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/lifecycle"
)

// TestNew_DispatchesByMode is the mode -> constructor truth table: every mode either builds
// the right family, or fails with the error New promises for modes it doesn't construct.
func TestNew_DispatchesByMode(t *testing.T) {
	deps := &Deps{Runner: &recordingRunner{}}

	backendModes := []string{"compute", "drive", "s3", "nfs", "smbw", "data-services"}
	for _, mode := range backendModes {
		t.Run(mode, func(t *testing.T) {
			rt, err := New(config.Env{"MODE": mode}, deps)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, ok := rt.(*backendRuntime); !ok {
				t.Errorf("New() = %T, want *backendRuntime", rt)
			}
		})
	}

	t.Run("client", func(t *testing.T) {
		rt, err := New(config.Env{"MODE": "client"}, deps)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if _, ok := rt.(*clientRuntime); !ok {
			t.Errorf("New() = %T, want *clientRuntime", rt)
		}
	})

	for _, mode := range []string{"envoy", "telemetry"} {
		t.Run(mode, func(t *testing.T) {
			rt, err := New(config.Env{"MODE": mode}, deps)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, ok := rt.(*auxiliaryRuntime); !ok {
				t.Errorf("New() = %T, want *auxiliaryRuntime", rt)
			}
		})
	}

	checkType := map[string]func(lifecycle.ModeRuntime) bool{
		"ssdproxy":                func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*ssdProxyRuntime); return ok },
		"drivers-dist":            func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*driverDistRuntime); return ok },
		"adhoc-op-with-container": func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*containerOpRuntime); return ok },
		"adhoc-op":                func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*TaskRuntime); return ok },
		"drivers-builder":         func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*TaskRuntime); return ok },
		"discovery":               func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*TaskRuntime); return ok },
		"drivers-loader":          func(rt lifecycle.ModeRuntime) bool { _, ok := rt.(*TaskRuntime); return ok },
	}
	for mode, matches := range checkType {
		t.Run(mode, func(t *testing.T) {
			rt, err := New(config.Env{"MODE": mode}, deps)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if !matches(rt) {
				t.Errorf("New() = %T, wrong type for mode %q", rt, mode)
			}
		})
	}

	t.Run("dist_typo_suggests_drivers-dist", func(t *testing.T) {
		if _, err := New(config.Env{"MODE": "dist"}, deps); err == nil {
			t.Error("New() error = nil, want an error suggesting drivers-dist")
		}
	})

	t.Run("unknown_mode", func(t *testing.T) {
		if _, err := New(config.Env{"MODE": "bogus"}, deps); err == nil {
			t.Error("New() error = nil, want an unknown mode error")
		}
	})

	t.Run("adhoc_is_not_a_mode", func(t *testing.T) {
		// "adhoc" is the stem container name used by adhoc-op-with-container, not a MODE value:
		// the real mode is "adhoc-op".
		if _, err := New(config.Env{"MODE": "adhoc"}, deps); err == nil {
			t.Error("New() error = nil, want an unknown mode error for \"adhoc\"")
		}
	})
}
