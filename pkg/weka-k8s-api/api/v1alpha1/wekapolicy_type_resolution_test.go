package v1alpha1

import "testing"

func TestWekaPolicySpecGetType(t *testing.T) {
	cases := []struct {
		name       string
		spec       WekaPolicySpec
		want       WekaPolicyType
		wantConfig bool
		wantErr    bool
	}{
		{
			name: "explicit type wins over an unrelated payload",
			spec: WekaPolicySpec{
				Type:    WekaPolicyTypeEnsureNICs,
				Payload: PolicyPayload{SignDrives: &SignDrivesPayload{}},
			},
			want: WekaPolicyTypeEnsureNICs,
		},
		{
			name: "explicit type with no payload at all",
			spec: WekaPolicySpec{Type: WekaPolicyTypeDiscoverDrives},
			want: WekaPolicyTypeDiscoverDrives,
		},
		{
			name: "derive sign-drives",
			spec: WekaPolicySpec{Payload: PolicyPayload{SignDrives: &SignDrivesPayload{}}},
			want: WekaPolicyTypeSignDrives,
		},
		{
			name: "derive discover-drives",
			spec: WekaPolicySpec{Payload: PolicyPayload{DiscoverDrives: &DiscoverDrivesPayload{}}},
			want: WekaPolicyTypeDiscoverDrives,
		},
		{
			name: "derive ensure-nics",
			spec: WekaPolicySpec{Payload: PolicyPayload{EnsureNICs: &EnsureNICsPayload{}}},
			want: WekaPolicyTypeEnsureNICs,
		},
		{
			name: "derive enable-local-drivers-distribution",
			spec: WekaPolicySpec{Payload: PolicyPayload{DriverDistPayload: &DriverDistPayload{}}},
			want: WekaPolicyTypeEnableLocalDriversDistribution,
		},
		{
			name: "derive remote-traces-session",
			spec: WekaPolicySpec{Payload: PolicyPayload{RemoteTracesSession: &RemoteTracesSessionConfig{}}},
			want: WekaPolicyTypeRemoteTracesSession,
		},
		{
			name: "derive clean-stale-virtual-drives",
			spec: WekaPolicySpec{Payload: PolicyPayload{CleanStaleVirtualDrives: &CleanStaleVirtualDrivesPayload{}}},
			want: WekaPolicyTypeCleanStaleVirtualDrives,
		},
		{
			name:       "configuration is reported through the flag, not a type",
			spec:       WekaPolicySpec{Payload: PolicyPayload{Configuration: &ConfigurationPayload{}}},
			wantConfig: true,
		},
		{
			name:    "no type and no payload",
			spec:    WekaPolicySpec{},
			wantErr: true,
		},
		{
			name: "two runnable payloads are ambiguous",
			spec: WekaPolicySpec{Payload: PolicyPayload{
				SignDrives:     &SignDrivesPayload{},
				DiscoverDrives: &DiscoverDrivesPayload{},
			}},
			wantErr: true,
		},
		{
			name: "configuration alongside a runnable payload is ambiguous",
			spec: WekaPolicySpec{Payload: PolicyPayload{
				Configuration: &ConfigurationPayload{},
				SignDrives:    &SignDrivesPayload{},
			}},
			wantErr: true,
		},
		{
			name: "configuration with an explicit type is rejected",
			spec: WekaPolicySpec{
				Type:    WekaPolicyTypeSignDrives,
				Payload: PolicyPayload{Configuration: &ConfigurationPayload{}},
			},
			wantErr: true,
		},
		{
			name:    "schedulingConfig carries no type, so it is not a signal",
			spec:    WekaPolicySpec{Payload: PolicyPayload{SchedulingConfig: &SchedulingConfigPayload{}}},
			wantErr: true,
		},
		{
			name:    "waitForPolicies alone is not a signal",
			spec:    WekaPolicySpec{Payload: PolicyPayload{WaitForPolicies: []string{"other"}}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, isConfig, err := tc.spec.GetType()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got type %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("type = %q, want %q", got, tc.want)
			}
			if isConfig != tc.wantConfig {
				t.Errorf("isConfiguration = %v, want %v", isConfig, tc.wantConfig)
			}
		})
	}
}

// The method on WekaPolicy is what most callers reach for; it must agree with the spec's.
func TestWekaPolicyGetTypeDelegates(t *testing.T) {
	policy := &WekaPolicy{Spec: WekaPolicySpec{
		Payload: PolicyPayload{DriverDistPayload: &DriverDistPayload{}},
	}}

	got, isConfig, err := policy.GetType()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != WekaPolicyTypeEnableLocalDriversDistribution {
		t.Errorf("type = %q, want %q", got, WekaPolicyTypeEnableLocalDriversDistribution)
	}
	if isConfig || policy.IsConfiguration() {
		t.Error("configuration reported for a drivers-dist policy")
	}
}

func TestIsConfiguration(t *testing.T) {
	policy := &WekaPolicy{Spec: WekaPolicySpec{
		Payload: PolicyPayload{Configuration: &ConfigurationPayload{}},
	}}
	if !policy.IsConfiguration() {
		t.Error("IsConfiguration = false for a configuration policy")
	}
}
