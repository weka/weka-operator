package domain

import (
	"encoding/json"
	"testing"
)

func TestParseFeatureFlags(t *testing.T) {
	full, _ := json.Marshal(FeatureFlags{SsdProxyVidLabelAdd: true})
	flags, err := ParseFeatureFlags(full)
	if err != nil || !flags.SsdProxyVidLabelAdd {
		t.Fatalf("full file: flags=%+v err=%v", flags, err)
	}

	var m map[string]bool
	_ = json.Unmarshal(full, &m)
	delete(m, "ssd_proxy_vid_label_add")
	m["unknown_new_flag"] = true
	old, _ := json.Marshal(m)
	if _, err := ParseFeatureFlags(old); err == nil {
		t.Fatal("file without a known flag must be rejected")
	}

	if _, err := ParseFeatureFlags([]byte("not json")); err == nil {
		t.Fatal("invalid json must be rejected")
	}
}
