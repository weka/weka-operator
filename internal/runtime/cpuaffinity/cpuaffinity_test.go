package cpuaffinity

import (
	"os"
	"path/filepath"
	"testing"
)

// ---- expandRanges tests ----

func TestExpandRanges(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int
	}{
		{
			name:  "range and range",
			input: "0-3,8-11",
			want:  []int{0, 1, 2, 3, 8, 9, 10, 11},
		},
		{
			name:  "single value",
			input: "5",
			want:  []int{5},
		},
		{
			name:  "mixed single and range",
			input: "0,2-3",
			want:  []int{0, 2, 3},
		},
		{
			name:  "empty string returns empty",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace trimmed around segment",
			input: " 0-2 , 5 ",
			want:  []int{0, 1, 2, 5},
		},
		{
			name:  "single range",
			input: "4-7",
			want:  []int{4, 5, 6, 7},
		},
		{
			name:  "multiple singles",
			input: "0,4,8",
			want:  []int{0, 4, 8},
		},
		{
			name:  "range with single value equals",
			input: "3-3",
			want:  []int{3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandRanges(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("expandRanges(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("expandRanges(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---- parseCPUAllowedList tests ----

func TestParseCPUAllowedList(t *testing.T) {
	tests := []struct {
		name    string
		content string // file content; empty means file not created
		want    []int
		wantErr bool
		noFile  bool // pass a path that doesn't exist
	}{
		{
			name:    "Cpus_allowed_list with tab separator - range",
			content: "Name:\tsome_process\nCpus_allowed:\tff\nCpus_allowed_list:\t0-3\nVmRSS:\t1234 kB\n",
			want:    []int{0, 1, 2, 3},
		},
		{
			name:    "Cpus_allowed_list single cpu",
			content: "Cpus_allowed_list:\t5\n",
			want:    []int{5},
		},
		{
			name:    "Cpus_allowed_list multiple ranges",
			content: "Cpus_allowed_list:\t0-3,8-11\n",
			want:    []int{0, 1, 2, 3, 8, 9, 10, 11},
		},
		{
			name:    "file without Cpus_allowed_list line returns error",
			content: "Name:\ttest\nVmRSS:\t100 kB\n",
			wantErr: true,
		},
		{
			name:    "missing file returns error",
			noFile:  true,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.noFile {
				path = filepath.Join(t.TempDir(), "nonexistent_status")
			} else {
				dir := t.TempDir()
				path = filepath.Join(dir, "status")
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatalf("write test file: %v", err)
				}
			}

			got, err := parseCPUAllowedList(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCPUAllowedList() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if len(got) != len(tt.want) {
				t.Fatalf("parseCPUAllowedList() = %v (len %d), want %v (len %d)",
					got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("parseCPUAllowedList()[%d] = %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---- intsToCSV tests ----

func TestIntsToCSV(t *testing.T) {
	tests := []struct {
		name  string
		input []int
		want  string
	}{
		{name: "single element", input: []int{5}, want: "5"},
		{name: "multiple elements", input: []int{0, 3, 7}, want: "0,3,7"},
		{name: "empty slice", input: []int{}, want: ""},
		{name: "nil slice", input: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intsToCSV(tt.input)
			if got != tt.want {
				t.Errorf("intsToCSV(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- mapsEqual tests ----

func makeIntSet(vals ...int) map[int]struct{} {
	m := make(map[int]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}

func TestMapsEqual(t *testing.T) {
	tests := []struct {
		name string
		a    map[int]struct{}
		b    map[int]struct{}
		want bool
	}{
		{
			name: "equal maps",
			a:    makeIntSet(1, 2, 3),
			b:    makeIntSet(3, 2, 1),
			want: true,
		},
		{
			name: "different lengths — a larger",
			a:    makeIntSet(1, 2, 3),
			b:    makeIntSet(1, 2),
			want: false,
		},
		{
			name: "different lengths — b larger",
			a:    makeIntSet(1, 2),
			b:    makeIntSet(1, 2, 3),
			want: false,
		},
		{
			name: "disjoint keys",
			a:    makeIntSet(1, 2),
			b:    makeIntSet(3, 4),
			want: false,
		},
		{
			name: "both empty",
			a:    makeIntSet(),
			b:    makeIntSet(),
			want: true,
		},
		{
			name: "nil maps",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "one nil one empty",
			a:    nil,
			b:    makeIntSet(),
			want: true,
		},
		{
			name: "partial overlap",
			a:    makeIntSet(1, 2, 3),
			b:    makeIntSet(1, 2, 4),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapsEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("mapsEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
