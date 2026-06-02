package config

import (
	"testing"
)

// ---- parseInt tests ----

func TestParseInt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "empty string returns 0", input: "", want: 0},
		{name: "valid positive", input: "42", want: 42},
		{name: "invalid string returns 0", input: "abc", want: 0},
		{name: "negative value", input: "-5", want: -5},
		{name: "zero string", input: "0", want: 0},
		{name: "whitespace returns 0", input: " ", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseInt(tt.input)
			if got != tt.want {
				t.Errorf("parseInt(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// ---- parseBool tests ----

func TestParseBool(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "true string", input: "true", want: true},
		{name: "1 string", input: "1", want: true},
		{name: "false string", input: "false", want: false},
		{name: "0 string", input: "0", want: false},
		{name: "empty string returns false", input: "", want: false},
		{name: "invalid string returns false", input: "abc", want: false},
		{name: "TRUE uppercase", input: "TRUE", want: true},
		{name: "FALSE uppercase", input: "FALSE", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBool(tt.input)
			if got != tt.want {
				t.Errorf("parseBool(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---- parseStringSlice tests ----

func TestParseStringSlice(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantLen int
		wantVal []string
	}{
		{
			name:    "empty string returns nil",
			input:   "",
			wantNil: true,
		},
		{
			name:    "three elements",
			input:   "a,b,c",
			wantLen: 3,
			wantVal: []string{"a", "b", "c"},
		},
		{
			name:    "single element",
			input:   "x",
			wantLen: 1,
			wantVal: []string{"x"},
		},
		{
			name:    "trailing comma produces empty last element",
			input:   "a,",
			wantLen: 2,
			wantVal: []string{"a", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStringSlice(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("parseStringSlice(%q) = %v, want nil", tt.input, got)
				}
				return
			}
			if len(got) != tt.wantLen {
				t.Fatalf("parseStringSlice(%q) len = %d, want %d (got %v)", tt.input, len(got), tt.wantLen, got)
			}
			for i, want := range tt.wantVal {
				if got[i] != want {
					t.Errorf("parseStringSlice(%q)[%d] = %q, want %q", tt.input, i, got[i], want)
				}
			}
		})
	}
}

// ---- parseIntSlice tests ----

func TestParseIntSlice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int
	}{
		{
			name:  "trimmed spaces around values",
			input: "1, 2 ,3",
			want:  []int{1, 2, 3},
		},
		{
			name:  "zero string is kept",
			input: "0,1",
			want:  []int{0, 1},
		},
		{
			name:  "non-numeric values are dropped",
			input: "x,2",
			want:  []int{2},
		},
		{
			name:  "empty string returns empty",
			input: "",
			want:  []int{},
		},
		{
			name:  "all invalid returns empty",
			input: "a,b,c",
			want:  []int{},
		},
		{
			name:  "negative value preserved",
			input: "-1,2",
			want:  []int{-1, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIntSlice(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseIntSlice(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("parseIntSlice(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFeatureFlagsFromBitmap(t *testing.T) {
	tests := []struct {
		name   string
		bitmap string
		// expected field values
		tracesOverridePartial    bool
		tracesOverrideSlash      bool
		supportsBindingNotAll    bool
		agentValidate60Ports     bool
		allowPerContainerDrivers bool
		wekaGetCopyLocalDrivers  bool
		driverSupportsAutoDrain  bool
		ssdProxyIommuSupport     bool
		ssdProxyIncludesDpdk     bool
	}{
		{
			name:   "invalid base64 returns all false",
			bitmap: "not-valid-base64!!!",
		},
		{
			name:   "all-zero byte returns all false",
			bitmap: "AA==", // 0x00
		},
		{
			name:                  "bit 0 sets TracesOverridePartialSupport only",
			bitmap:                "AQ==", // 0x01
			tracesOverridePartial: true,
		},
		{
			name:                "bit 1 sets TracesOverrideInSlashTraces only",
			bitmap:              "Ag==", // 0x02
			tracesOverrideSlash: true,
		},
		{
			name:                  "bit 2 sets SupportsBindingToNotAllInterfaces only",
			bitmap:                "BA==", // 0x04
			supportsBindingNotAll: true,
		},
		{
			name:                 "bit 7 sets SsdProxyIommuSupport only",
			bitmap:               "gA==", // 0x80
			ssdProxyIommuSupport: true,
		},
		{
			name:   "bit 8 is unused — maps to nothing",
			bitmap: "AAE=", // byte[0]=0x00, byte[1]=0x01 → bit 8 set
			// all flags remain false: bit 8 is explicitly unused
		},
		{
			name:                 "bit 9 sets SsdProxyIncludesDpdkMemory only",
			bitmap:               "AAI=", // byte[0]=0x00, byte[1]=0x02 → bit 9 set
			ssdProxyIncludesDpdk: true,
		},
		{
			// "Bw==" from 4.4.10 release: 0x07 = bits 0,1,2
			name:                  "real bitmap Bw== from 4.4.10",
			bitmap:                "Bw==",
			tracesOverridePartial: true,
			tracesOverrideSlash:   true,
			supportsBindingNotAll: true,
		},
		{
			// "Hw==" from 5.1.0 release: 0x1F = bits 0,1,2,3,4
			name:                     "real bitmap Hw== from 5.1.0",
			bitmap:                   "Hw==",
			tracesOverridePartial:    true,
			tracesOverrideSlash:      true,
			supportsBindingNotAll:    true,
			agentValidate60Ports:     true,
			allowPerContainerDrivers: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := featureFlagsFromBitmap(tt.bitmap)

			check := func(field string, got, want bool) {
				t.Helper()
				if got != want {
					t.Errorf("%s: got %v, want %v", field, got, want)
				}
			}

			check("TracesOverridePartialSupport", got.TracesOverridePartialSupport, tt.tracesOverridePartial)
			check("TracesOverrideInSlashTraces", got.TracesOverrideInSlashTraces, tt.tracesOverrideSlash)
			check("SupportsBindingToNotAllInterfaces", got.SupportsBindingToNotAllInterfaces, tt.supportsBindingNotAll)
			check("AgentValidate60PortsPerContainer", got.AgentValidate60PortsPerContainer, tt.agentValidate60Ports)
			check("AllowPerContainerDriverInterfaces", got.AllowPerContainerDriverInterfaces, tt.allowPerContainerDrivers)
			check("WekaGetCopyLocalDriverFiles", got.WekaGetCopyLocalDriverFiles, tt.wekaGetCopyLocalDrivers)
			check("DriverSupportsAutoDrain", got.DriverSupportsAutoDrain, tt.driverSupportsAutoDrain)
			check("SsdProxyIommuSupport", got.SsdProxyIommuSupport, tt.ssdProxyIommuSupport)
			check("SsdProxyIncludesDpdkMemory", got.SsdProxyIncludesDpdkMemory, tt.ssdProxyIncludesDpdk)
		})
	}
}
