package domain

import (
	"testing"
)

func TestParseFeatureBitmap(t *testing.T) {
	// bytes 0x01,0x00,0x02 -> bits 0 and 17 set, 16 clear
	flags, err := parseFeatureBitmap("AQAC")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !flags.TracesOverridePartialSupport || !flags.SsdProxyVidLabelAdd {
		t.Errorf("bits 0 and 17 should be set: %+v", flags)
	}
	if flags.WekactlAsDefault || flags.S3ClusterCreateWithoutConfigFs {
		t.Errorf("bits 14, 16 and 19 should be clear: %+v", flags)
	}

	// The bitmap of a 6.2.0 build: bits 0-5, 9, 10, 12, 14-17 and 19.
	v620, err := parseFeatureBitmap("P9YL")
	if err != nil {
		t.Fatalf("6.2.0 bitmap: %v", err)
	}
	if !v620.S3ClusterCreateWithoutConfigFs || !v620.SsdProxyVidLabelAdd || v620.DriverSupportsAutoDrain {
		t.Errorf("6.2.0 bitmap: %+v", v620)
	}

	// A bitmap too short to hold a bit reads it as false rather than failing: that is what an
	// image predating the flag means.
	short, err := parseFeatureBitmap("AQ==") // one byte, bits 16/17 are past its end
	if err != nil {
		t.Fatalf("short bitmap: %v", err)
	}
	if !short.TracesOverridePartialSupport || short.SsdProxyVidLabelAdd {
		t.Errorf("short bitmap: %+v", short)
	}

	empty, err := parseFeatureBitmap("")
	if err != nil {
		t.Fatalf("empty bitmap: %v", err)
	}
	if *empty != (FeatureFlags{}) {
		t.Errorf("empty bitmap should yield no flags, got %+v", empty)
	}

	if _, err := parseFeatureBitmap("!!not base64!!"); err == nil {
		t.Error("invalid base64 must be rejected")
	}
}

func TestParseReleaseSpec(t *testing.T) {
	flags, err := ParseReleaseSpec([]byte(`{"feature_flags":"AQAC","version":"4.5.0"}`))
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if !flags.SsdProxyVidLabelAdd {
		t.Error("bit 17 should be set")
	}

	// A spec without the field is an image predating the bitmap, not an error.
	none, err := ParseReleaseSpec([]byte(`{"version":"4.0.0"}`))
	if err != nil {
		t.Fatalf("spec without feature_flags: %v", err)
	}
	if *none != (FeatureFlags{}) {
		t.Errorf("expected no flags, got %+v", none)
	}

	if _, err := ParseReleaseSpec([]byte("not json")); err == nil {
		t.Error("invalid json must be rejected")
	}
}
