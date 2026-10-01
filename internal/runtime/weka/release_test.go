package weka

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// bitsSet0271214 is the flag set fixture release_bits_0_2_7_12_14.spec decodes to.
var bitsSet0271214 = domain.FeatureFlags{
	TracesOverridePartialSupport:      true,
	SupportsBindingToNotAllInterfaces: true,
	SsdProxyIommuSupport:              true,
	WekaManagesNonIonodeAffinity:      true,
	WekactlAsDefault:                  true,
}

// bitsSet012 is the flag set fixture release_unknown_fields.spec ("Bw==") decodes to.
var bitsSet012 = domain.FeatureFlags{
	TracesOverridePartialSupport:      true,
	TracesOverrideInSlashTraces:       true,
	SupportsBindingToNotAllInterfaces: true,
}

func TestReadReleaseSpecDecodesBitmap(t *testing.T) {
	dir := copySpecFixture(t, "release_bits_0_2_7_12_14.spec")
	spec, err := ReadReleaseSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.FeatureFlags != bitsSet0271214 {
		t.Errorf("got %+v, want %+v", spec.FeatureFlags, bitsSet0271214)
	}
}

func TestReadReleaseSpecNoBitmap(t *testing.T) {
	dir := copySpecFixture(t, "release_no_bitmap.spec")
	spec, err := ReadReleaseSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.FeatureFlags != (domain.FeatureFlags{}) {
		t.Errorf("got %+v, want all-false", spec.FeatureFlags)
	}
}

func TestReadReleaseSpecMalformed(t *testing.T) {
	dir := copySpecFixture(t, "release_malformed.spec")
	if _, err := ReadReleaseSpec(dir); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestReadReleaseSpecMalformedBase64(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "bad.spec", `{"feature_flags": "not-valid-base64!"}`)
	if _, err := ReadReleaseSpec(dir); err == nil {
		t.Fatal("expected error for malformed base64")
	}
}

func TestReadReleaseSpecZeroFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadReleaseSpec(dir); err == nil {
		t.Fatal("expected error for zero *.spec files")
	}
}

func TestReadReleaseSpecTwoFiles(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "a.spec", `{"feature_flags": "Bw=="}`)
	writeSpec(t, dir, "b.spec", `{"feature_flags": "Bw=="}`)
	if _, err := ReadReleaseSpec(dir); err == nil {
		t.Fatal("expected error for two *.spec files")
	}
}

func TestReadReleaseSpecUnknownFieldsIgnored(t *testing.T) {
	dir := copySpecFixture(t, "release_unknown_fields.spec")
	spec, err := ReadReleaseSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "4.4.10" {
		t.Errorf("Version: got %q, want %q", spec.Version, "4.4.10")
	}
	if spec.FeatureFlags != bitsSet012 {
		t.Errorf("got %+v, want %+v", spec.FeatureFlags, bitsSet012)
	}
}

// copySpecFixture copies the named testdata fixture into a fresh temp dir so
// ReadReleaseSpec's glob sees exactly that one *.spec file.
func copySpecFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeSpec(t, dir, name, string(data))
	return dir
}

func writeSpec(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
