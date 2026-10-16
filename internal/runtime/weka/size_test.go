package weka

import "testing"

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1GiB":   1 << 30,
		"512MiB": 512 << 20,
		"2GB":    2e9,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseSize("bogus"); err == nil {
		t.Error("expected error for invalid size string")
	}
}
