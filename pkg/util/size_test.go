package util

import "testing"

func TestFormatTlcQlcColumn(t *testing.T) {
	tests := []struct {
		name   string
		tlcGiB int
		qlcGiB int
		want   string
	}{
		{name: "both same unit", tlcGiB: 28 * 1024, qlcGiB: 28 * 1024, want: "T/Q 28.0/28.0 TiB"},
		{name: "both differing units", tlcGiB: 512, qlcGiB: 2 * 1024, want: "T/Q 512.0GiB/2.0TiB"},
		{name: "tlc only", tlcGiB: 7 * 1024, qlcGiB: 0, want: "T 7.0TiB"},
		{name: "qlc only", tlcGiB: 0, qlcGiB: 20 * 1024, want: "Q 20.0TiB"},
		{name: "neither", tlcGiB: 0, qlcGiB: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatTlcQlcColumn(tt.tlcGiB, tt.qlcGiB); got != tt.want {
				t.Errorf("FormatTlcQlcColumn(%d, %d) = %q, want %q", tt.tlcGiB, tt.qlcGiB, got, tt.want)
			}
		})
	}
}
