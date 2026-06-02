package ports

import (
	"testing"
)

// ---- getFreeSubrange tests ----

func TestGetFreeSubrange(t *testing.T) {
	tests := []struct {
		name    string
		base    int
		top     int
		size    int
		inUse   map[int]struct{}
		exclude []int
		want    int
		wantErr bool
	}{
		{
			name:    "empty inUse returns base",
			base:    1000,
			top:     1200,
			size:    10,
			inUse:   map[int]struct{}{},
			exclude: nil,
			want:    1000,
		},
		{
			name:    "used port mid-window forces jump past it",
			base:    1000,
			top:     1200,
			size:    10,
			inUse:   map[int]struct{}{1005: {}},
			exclude: nil,
			// start=1000: scans to 1005 (used) → jump start=1005, loop increments to 1006
			want: 1006,
		},
		{
			name:    "excluded port at start is skipped",
			base:    1000,
			top:     1200,
			size:    10,
			inUse:   map[int]struct{}{},
			exclude: []int{1000},
			// 1000 excluded at start check → start=1001
			want: 1001,
		},
		{
			name:    "excluded port mid-window causes jump",
			base:    1000,
			top:     1200,
			size:    10,
			inUse:   map[int]struct{}{},
			exclude: []int{1007},
			// start=1000: scans to 1007 (excluded) → jump to 1007, loop increments to 1008
			want: 1008,
		},
		{
			name:    "agentPort as exclude skips that window",
			base:    1000,
			top:     1200,
			size:    5,
			inUse:   map[int]struct{}{},
			exclude: []int{1003}, // agentPort in the middle of first window
			// 1000 is not excluded; scans 1000..1004, hits 1003 (excluded) → jump to 1003, incr to 1004
			// then 1004 start: check 1004..1008 — all free → return 1004
			want: 1004,
		},
		{
			name:    "no window fits returns error",
			base:    1000,
			top:     1010,
			size:    20,
			inUse:   map[int]struct{}{},
			exclude: nil,
			wantErr: true,
		},
		{
			name:    "window exactly at top-size boundary",
			base:    1000,
			top:     1010,
			size:    10,
			inUse:   map[int]struct{}{},
			exclude: nil,
			// start <= top-size = 1000, exactly one window [1000,1010)
			want: 1000,
		},
		{
			name:    "window starting one before the boundary",
			base:    995,
			top:     1010,
			size:    10,
			inUse:   map[int]struct{}{},
			exclude: nil,
			// many windows fit; first free is 995
			want: 995,
		},
		{
			name: "all ports in range used returns error",
			base: 1000,
			top:  1005,
			size: 3,
			inUse: map[int]struct{}{
				1000: {}, 1001: {}, 1002: {}, 1003: {}, 1004: {},
			},
			exclude: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getFreeSubrange(tt.base, tt.top, tt.size, tt.inUse, tt.exclude)
			if (err != nil) != tt.wantErr {
				t.Fatalf("getFreeSubrange() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("getFreeSubrange() = %d, want %d", got, tt.want)
			}
		})
	}
}

// ---- findFreePort tests ----

func TestFindFreePort(t *testing.T) {
	tests := []struct {
		name    string
		base    int
		top     int
		inUse   map[int]struct{}
		exclude []int
		want    int
		wantErr bool
	}{
		{
			name:    "first free port returned",
			base:    2000,
			top:     2100,
			inUse:   map[int]struct{}{},
			exclude: nil,
			want:    2000,
		},
		{
			name:    "all ports used returns error",
			base:    2000,
			top:     2003,
			inUse:   map[int]struct{}{2000: {}, 2001: {}, 2002: {}},
			want:    0,
			wantErr: true,
		},
		{
			name:    "excluded port skipped",
			base:    3000,
			top:     3100,
			inUse:   map[int]struct{}{},
			exclude: []int{3000, 3001},
			want:    3002,
		},
		{
			name:    "base equals top returns error (empty range)",
			base:    5000,
			top:     5000,
			inUse:   map[int]struct{}{},
			exclude: nil,
			wantErr: true,
		},
		{
			name:    "used and excluded combination",
			base:    4000,
			top:     4010,
			inUse:   map[int]struct{}{4000: {}, 4001: {}},
			exclude: []int{4002},
			want:    4003,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findFreePort(tt.base, tt.top, tt.inUse, tt.exclude)
			if (err != nil) != tt.wantErr {
				t.Fatalf("findFreePort() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("findFreePort() = %d, want %d", got, tt.want)
			}
		})
	}
}
