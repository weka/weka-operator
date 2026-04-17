package admission

import "testing"

func TestModeFor(t *testing.T) {
	def := PolicyDefaults{Strict: Error, Relaxed: Warn}

	tests := []struct {
		name     string
		mode     string
		override string
		want     Mode
	}{
		{"strict + no override → defaults.Strict", "strict", "", Error},
		{"relaxed + no override → defaults.Relaxed", "relaxed", "", Warn},
		{"strict + default override → defaults.Strict", "strict", "default", Error},
		{"relaxed + default override → defaults.Relaxed", "relaxed", "default", Warn},
		{"override warn wins over strict mode", "strict", "warn", Warn},
		{"override error wins over relaxed mode", "relaxed", "error", Error},
		{"override is case-insensitive: WARN", "strict", "WARN", Warn},
		{"override is case-insensitive: Error", "relaxed", "Error", Error},
		{"mode is case-insensitive: STRICT", "STRICT", "", Error},
		{"unknown mode falls back to Relaxed", "weird", "", Warn},
		{"unknown override falls through to mode", "strict", "gibberish", Error},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modeFor(tt.mode, tt.override, def)
			if got != tt.want {
				t.Errorf("modeFor(mode=%q, override=%q, %+v) = %v, want %v",
					tt.mode, tt.override, def, got, tt.want)
			}
		})
	}
}

func TestModeFor_EqualDefaults(t *testing.T) {
	t.Run("advisory equal in both modes", func(t *testing.T) {
		def := PolicyDefaults{Strict: Warn, Relaxed: Warn}
		if got := modeFor("strict", "", def); got != Warn {
			t.Errorf("got %v, want Warn", got)
		}
		if got := modeFor("relaxed", "", def); got != Warn {
			t.Errorf("got %v, want Warn", got)
		}
	})
	t.Run("impossible equal in both modes", func(t *testing.T) {
		def := PolicyDefaults{Strict: Error, Relaxed: Error}
		if got := modeFor("strict", "", def); got != Error {
			t.Errorf("got %v, want Error", got)
		}
		if got := modeFor("relaxed", "", def); got != Error {
			t.Errorf("got %v, want Error", got)
		}
	})
}
