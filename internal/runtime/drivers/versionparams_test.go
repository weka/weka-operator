package drivers

import "testing"

// TestResolveVersionParams validates faithful parity with the Python
// VERSION_TO_DRIVERS_MAP_WEKAFS / DEFAULT_PARAMS lookup at weka_runtime.py:1254-1351.
func TestResolveVersionParams(t *testing.T) {
	tests := []struct {
		name                string
		imageName           string
		wantHandling        bool   // WekaDriversHandling
		wantSkipUio         bool   // ShouldSkipUioPciGeneric
		wantDependencies    string // EffectiveDependencies()
		wantWekafs          string // exact Wekafs (only checked when non-empty)
		wantEffectiveUioVer string // EffectiveUioPciGenericVersion() (only checked when non-empty)
	}{
		{
			// Unknown tag → DEFAULT_PARAMS: new handling, uio_pci_generic=False (skip), default deps.
			name:             "unknown image falls back to DEFAULT_PARAMS",
			imageName:        "quay.io/weka/weka-in-container:9.9.9-unknown",
			wantHandling:     true,
			wantSkipUio:      true,
			wantDependencies: DefaultDependencyVersion,
		},
		{
			// Plain 4.3.3 → legacy handling, uio_pci_generic=False, explicit dependencies.
			name:             "plain 4.3.3 is legacy with explicit deps",
			imageName:        "quay.io/weka/weka-in-container:4.3.3",
			wantHandling:     false,
			wantSkipUio:      true,
			wantDependencies: "7955984e4bce9d8b",
			wantWekafs:       "cbd05f716a3975f7-GW_556972ab1ad2a29b0db5451e9db18748",
		},
		{
			// 4.3.x-dev → legacy handling, uio_pci_generic=False, dev deps.
			name:             "4.3.x-dev is legacy and skips uio",
			imageName:        "quay.io/weka/weka-in-container:4.3.1.29791-9f57657d1fb70e71a3fb914ff7d75eee-dev",
			wantHandling:     false,
			wantSkipUio:      true,
			wantDependencies: "6b519d501ea82063",
		},
		{
			// 4.2.x map entry → legacy handling, uio_pci_generic key ABSENT → load it (do not skip).
			name:             "4.2.7.64 entry loads uio and uses default deps",
			imageName:        "quay.io/weka/weka-in-container:4.2.7.64-k8so-beta.10",
			wantHandling:     false,
			wantSkipUio:      false,
			wantDependencies: DefaultDependencyVersion,
		},
		{
			// s3multitenancy wholesale override (matched by substring, not tag) →
			// legacy handling, uio_pci_generic is a version string → load that version (do not skip).
			name:                "4.2.7.64-s3multitenancy wholesale override",
			imageName:           "quay.io/weka/weka-in-container:4.2.7.64-s3multitenancy.5",
			wantHandling:        false,
			wantSkipUio:         false,
			wantDependencies:    DefaultDependencyVersion,
			wantWekafs:          "1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
			wantEffectiveUioVer: "1.0.0-929f279ce026ddd2e31e281b93b38f52",
		},
		{
			// Tag extraction: no colon → whole string is the tag (still resolves via override path here).
			name:             "image without registry colon",
			imageName:        "4.3.3",
			wantHandling:     false,
			wantSkipUio:      true,
			wantDependencies: "7955984e4bce9d8b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := ResolveVersionParams(tc.imageName)
			if p.WekaDriversHandling != tc.wantHandling {
				t.Errorf("WekaDriversHandling = %v, want %v", p.WekaDriversHandling, tc.wantHandling)
			}
			if got := p.ShouldSkipUioPciGeneric(); got != tc.wantSkipUio {
				t.Errorf("ShouldSkipUioPciGeneric() = %v, want %v", got, tc.wantSkipUio)
			}
			if got := p.EffectiveDependencies(); got != tc.wantDependencies {
				t.Errorf("EffectiveDependencies() = %q, want %q", got, tc.wantDependencies)
			}
			if tc.wantWekafs != "" && p.Wekafs != tc.wantWekafs {
				t.Errorf("Wekafs = %q, want %q", p.Wekafs, tc.wantWekafs)
			}
			if tc.wantEffectiveUioVer != "" {
				if got := p.EffectiveUioPciGenericVersion(); got != tc.wantEffectiveUioVer {
					t.Errorf("EffectiveUioPciGenericVersion() = %q, want %q", got, tc.wantEffectiveUioVer)
				}
			}
		})
	}
}

// TestEffectiveUioPciGenericVersionFallback verifies the constant fallback when no version is set.
func TestEffectiveUioPciGenericVersionFallback(t *testing.T) {
	p := ResolveVersionParams("quay.io/weka/weka-in-container:9.9.9-unknown")
	if got := p.EffectiveUioPciGenericVersion(); got != UioPciGenericDriverVersion {
		t.Errorf("EffectiveUioPciGenericVersion() = %q, want fallback %q", got, UioPciGenericDriverVersion)
	}
}

// TestEffectiveUioPciGenericVersion covers both the set-value and fallback paths explicitly.
func TestEffectiveUioPciGenericVersion(t *testing.T) {
	tests := []struct {
		name   string
		params VersionParams
		want   string
	}{
		{
			name:   "UioPciGeneric set — returns set value",
			params: VersionParams{UioPciGeneric: "1.0.0-929f279ce026ddd2e31e281b93b38f52"},
			want:   "1.0.0-929f279ce026ddd2e31e281b93b38f52",
		},
		{
			name:   "UioPciGeneric empty — falls back to global constant",
			params: VersionParams{UioPciGeneric: ""},
			want:   UioPciGenericDriverVersion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.params.EffectiveUioPciGenericVersion()
			if got != tt.want {
				t.Errorf("EffectiveUioPciGenericVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEffectiveDependencies covers both the set-value and fallback paths explicitly.
func TestEffectiveDependencies(t *testing.T) {
	tests := []struct {
		name   string
		params VersionParams
		want   string
	}{
		{
			name:   "Dependencies set — returns set value",
			params: VersionParams{Dependencies: "7955984e4bce9d8b"},
			want:   "7955984e4bce9d8b",
		},
		{
			name:   "Dependencies empty — falls back to global constant",
			params: VersionParams{Dependencies: ""},
			want:   DefaultDependencyVersion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.params.EffectiveDependencies()
			if got != tt.want {
				t.Errorf("EffectiveDependencies() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestShouldSkipUioPciGeneric verifies it directly reflects UioPciGenericDisabled.
func TestShouldSkipUioPciGeneric(t *testing.T) {
	tests := []struct {
		name     string
		disabled bool
		want     bool
	}{
		{name: "UioPciGenericDisabled true → skip", disabled: true, want: true},
		{name: "UioPciGenericDisabled false → do not skip", disabled: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := VersionParams{UioPciGenericDisabled: tt.disabled}
			got := p.ShouldSkipUioPciGeneric()
			if got != tt.want {
				t.Errorf("ShouldSkipUioPciGeneric() = %v, want %v", got, tt.want)
			}
		})
	}
}
