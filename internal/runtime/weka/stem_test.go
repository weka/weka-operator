package weka

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/process"
)

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

// TestStartStemCLI verifies StartStemCLI launches "weka local start" through a real
// process.Manager rather than an unwaited direct exec. There is no "weka" binary in the test
// environment, so the launch itself fails; what's under test is that the command reaches
// Manager.StartProcess with the right shape.
func TestStartStemCLI(t *testing.T) {
	ctx := context.Background()
	pm := process.NewManager(ctx)

	handle, err := StartStemCLI(ctx, pm, "envoy")
	if err == nil {
		t.Fatalf("expected launch error for missing weka binary, got handle %v", handle)
	}
	var execErr *process.ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *process.ExecError, got %T: %v", err, err)
	}
	if execErr.Kind != process.FailureLaunch {
		t.Errorf("Kind = %v, want FailureLaunch", execErr.Kind)
	}
	if execErr.Cmd != "weka local start" {
		t.Errorf("Cmd = %q, want %q", execErr.Cmd, "weka local start")
	}
}
