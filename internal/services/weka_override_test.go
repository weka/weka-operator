package services

import (
	"encoding/json"
	"testing"
)

func TestParseOverrideID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"ManualOverrideId<12>", "12"},
		{"7", "7"},
		{"bogus", ""},
		{"", ""},
		// An unrecognised shape must be rejected outright, not partially matched: the extracted
		// id goes straight to "weka debug override remove --id".
		{"Override2Id<15>", ""},
		{"ManualOverrideId<12> trailing", ""},
		// Mismatched delimiters: prefix without closing '>', or '>' without the prefix.
		{"ManualOverrideId<12", ""},
		{"12>", ""},
		{"ManualOverrideId<>", ""},
	} {
		got, err := parseOverrideID(tc.in)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%q: got %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWekaOverrideDecode pins the JSON wire format that `weka debug override list -J` produces.
// A tag mismatch on Enabled would decode every row as false, and ensureOverride would then treat
// the operator's own row as never set, appending a new one every reconcile forever.
func TestWekaOverrideDecode(t *testing.T) {
	const payload = `[
		{
			"override_id": "ManualOverrideId<1>",
			"key": "weka_cloud_ca_cert_path",
			"value": "\"/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem\"",
			"nodes": "ALL",
			"reason": "weka-operator: Weka Home CA bundle staged from wekaHome.cacertSecret",
			"enabled": true,
			"time_created": "2026-01-01T15:52:44Z"
		},
		{
			"override_id": "ManualOverrideId<2>",
			"key": "weka_cloud_ca_cert_path",
			"value": "\"/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem\"",
			"nodes": "ALL",
			"enabled": false,
			"time_created": "2026-01-01T15:53:00Z"
		}
	]`

	var overrides []WekaOverride
	if err := json.Unmarshal([]byte(payload), &overrides); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(overrides) != 2 {
		t.Fatalf("got %d overrides, want 2", len(overrides))
	}
	want0 := WekaOverride{
		OverrideID: "ManualOverrideId<1>",
		Key:        "weka_cloud_ca_cert_path",
		Value:      `"/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem"`,
		Enabled:    true,
	}
	if overrides[0] != want0 {
		t.Errorf("row 0: got %+v, want %+v", overrides[0], want0)
	}
	if overrides[1].Enabled {
		t.Errorf("row 1: got enabled=true, want false")
	}
}

func TestUnquoteOverrideValue(t *testing.T) {
	const path = "/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem"
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"quoted value is unwrapped", `"` + path + `"`, path},
		{"bare value is unchanged", path, path},
		{"unbalanced quote is left alone", `"` + path, `"` + path},
		{"empty stays empty", "", ""},
	} {
		if got := unquoteOverrideValue(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
