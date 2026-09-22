package weka

import "testing"

func TestContainsContainerName(t *testing.T) {
	tests := []struct {
		name    string
		psJSON  string
		target  string
		want    bool
		wantErr bool
	}{
		{
			name:   "exact match found",
			psJSON: `[{"name":"envoy"},{"name":"telemetry"}]`,
			target: "envoy",
			want:   true,
		},
		{
			name:   "no match",
			psJSON: `[{"name":"telemetry"}]`,
			target: "envoy",
			want:   false,
		},
		{
			name:   "substring is not a match",
			psJSON: `[{"name":"envoy-sidecar"}]`,
			target: "envoy",
			want:   false,
		},
		{
			name:   "empty list",
			psJSON: `[]`,
			target: "envoy",
			want:   false,
		},
		{
			name:    "invalid JSON errors",
			psJSON:  `not json`,
			target:  "envoy",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := containsContainerName([]byte(tt.psJSON), tt.target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("containsContainerName() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("containsContainerName() = %v, want %v", got, tt.want)
			}
		})
	}
}
