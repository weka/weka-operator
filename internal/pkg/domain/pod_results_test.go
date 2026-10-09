package domain

import (
	"encoding/json"
	"testing"
)

func TestDriveNodeResults_JSONContract(t *testing.T) {
	errMsg := "some error"
	tests := []struct {
		name           string
		input          DriveNodeResults
		wantErrJSON    string // "null" or `"some error"`
		wantProxyField bool
	}{
		{
			name:        "nil error encodes as null",
			input:       DriveNodeResults{Err: nil, Drives: []DriveInfo{}, RawDrives: []DriveRawInfo{}},
			wantErrJSON: "null",
		},
		{
			name:        "non-nil error encodes as string",
			input:       DriveNodeResults{Err: &errMsg, Drives: []DriveInfo{}, RawDrives: []DriveRawInfo{}},
			wantErrJSON: `"some error"`,
		},
		{
			name:           "empty proxy_drives is omitted",
			input:          DriveNodeResults{Err: nil, ProxyDrives: nil},
			wantErrJSON:    "null",
			wantProxyField: false,
		},
		{
			name: "non-empty proxy_drives is present",
			input: DriveNodeResults{Err: nil, ProxyDrives: []SharedDriveInfo{
				{PhysicalUUID: "abc", Serial: "SN1"},
			}},
			wantErrJSON:    "null",
			wantProxyField: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("unmarshal to map: %v", err)
			}

			// Required fields always present
			for _, key := range []string{"err", "drives", "raw_drives"} {
				if _, ok := raw[key]; !ok {
					t.Errorf("missing required JSON field %q", key)
				}
			}

			if string(raw["err"]) != tt.wantErrJSON {
				t.Errorf("err field: got %s, want %s", raw["err"], tt.wantErrJSON)
			}

			_, hasProxy := raw["proxy_drives"]
			if hasProxy != tt.wantProxyField {
				t.Errorf("proxy_drives present=%v, want %v", hasProxy, tt.wantProxyField)
			}
		})
	}
}

func TestDriveRawInfo_JSONContract(t *testing.T) {
	input := DriveRawInfo{
		SerialId:    "SN123",
		Path:        "/dev/sda",
		IsMounted:   true,
		CapacityGiB: 512,
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantKeys := map[string]string{
		"serial_id":    `"SN123"`,
		"path":         `"/dev/sda"`,
		"is_mounted":   "true",
		"capacity_gib": "512",
	}
	for k, want := range wantKeys {
		got, ok := raw[k]
		if !ok {
			t.Errorf("missing field %q", k)
			continue
		}
		if string(got) != want {
			t.Errorf("field %q: got %s, want %s", k, got, want)
		}
	}
}

func TestResignDrivesResult_JSONContract(t *testing.T) {
	tests := []struct {
		name      string
		input     ResignDrivesResult
		wantNoErr bool
	}{
		{
			name:      "empty Err is omitted",
			input:     ResignDrivesResult{Err: "", Drives: []string{"SN1", "SN2"}},
			wantNoErr: true,
		},
		{
			name:      "non-empty Err is present",
			input:     ResignDrivesResult{Err: "failed", Drives: []string{}},
			wantNoErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if _, ok := raw["drives"]; !ok {
				t.Error("missing required field 'drives'")
			}

			_, hasErr := raw["err"]
			if tt.wantNoErr && hasErr {
				t.Error("'err' field present when empty (should be omitted)")
			}
			if !tt.wantNoErr && !hasErr {
				t.Error("'err' field absent when non-empty")
			}
		})
	}
}

func TestBuiltDriversResult_JSONContract(t *testing.T) {
	// Err has no omitempty — operator always reads it
	input := BuiltDriversResult{
		WekaVersion:     "5.1.0",
		KernelSignature: "abc123",
		Err:             "",
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"weka_version", "kernel_signature", "weka_pack_not_supported", "no_weka_drivers_handling", "err"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing field %q (should always be present)", key)
		}
	}

	if string(raw["err"]) != `""` {
		t.Errorf("err field: got %s, want empty string", raw["err"])
	}
}

func TestFeatureFlagsResult_JSONContract(t *testing.T) {
	t.Run("nil feature_flags encodes as null", func(t *testing.T) {
		data, err := json.Marshal(FeatureFlagsResult{FeatureFlags: nil})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := raw["feature_flags"]; !ok {
			t.Error("missing field 'feature_flags'")
		}
		if string(raw["feature_flags"]) != "null" {
			t.Errorf("feature_flags: got %s, want null", raw["feature_flags"])
		}
	})

	t.Run("non-nil feature_flags encodes as object", func(t *testing.T) {
		ff := &FeatureFlags{TracesOverridePartialSupport: true}
		data, err := json.Marshal(FeatureFlagsResult{FeatureFlags: ff})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(raw["feature_flags"]) == "null" {
			t.Error("feature_flags should not be null for non-nil struct")
		}
	})
}
