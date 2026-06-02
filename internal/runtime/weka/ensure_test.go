package weka

import (
	"testing"
)

// ---- parseNonDatapathCoreIDs tests ----

func TestParseNonDatapathCoreIDs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{
			name:  "explicit list matches Python int(c) for c in s.split(',')",
			input: "2,3,6,7",
			want:  []int{2, 3, 6, 7},
		},
		{
			name:  "single core ID",
			input: "4",
			want:  []int{4},
		},
		{
			name:  "spaces around values are trimmed",
			input: " 1 , 2 , 3 ",
			want:  []int{1, 2, 3},
		},
		{
			name:  "zero is a valid core ID",
			input: "0,1",
			want:  []int{0, 1},
		},
		{
			name:    "non-numeric value returns error",
			input:   "1,abc,3",
			wantErr: true,
		},
		{
			name:    "empty string returns error",
			input:   "",
			wantErr: true,
		},
		{
			name:    "only commas returns error",
			input:   ",,,",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNonDatapathCoreIDs(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseNonDatapathCoreIDs(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseNonDatapathCoreIDs(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("parseNonDatapathCoreIDs(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}
