package admission

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/validation"
)

// fakeValidator is a stub that returns a pre-canned violation for a given
// policy ID. It does not consult c or obj — the dispatcher's behaviour
// doesn't depend on what the validator inspects, only on what it returns.
type fakeValidator struct {
	id        string
	violation *field.Error // nil → validator passes
}

func (f *fakeValidator) ID() string { return f.id }

func (f *fakeValidator) Validate(_ context.Context, _ client.Client, _ runtime.Object) field.ErrorList {
	if f.violation == nil {
		return nil
	}
	return field.ErrorList{f.violation}
}

func violation(id string) *field.Error {
	return field.Invalid(field.NewPath("spec", "x"), 0, "violation from "+id)
}

// fakeUpdateValidator is a stub that returns a pre-canned violation for a
// given policy ID. It does not consult c, oldObj, or newObj.
type fakeUpdateValidator struct {
	id        string
	violation *field.Error // nil → validator passes
}

func (f *fakeUpdateValidator) ID() string { return f.id }

func (f *fakeUpdateValidator) ValidateUpdate(_ context.Context, _ client.Client, _, _ runtime.Object) field.ErrorList {
	if f.violation == nil {
		return nil
	}
	return field.ErrorList{f.violation}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name         string
		validators   []validation.Validator
		defaults     map[string]PolicyDefaults
		cfg          config.AdmissionPoliciesConfig
		wantWarnings int
		wantErrs     int
	}{
		{
			name:         "empty registry — no-op",
			validators:   nil,
			defaults:     nil,
			wantWarnings: 0,
			wantErrs:     0,
		},
		{
			name: "warn-mode policy with violation routes to warnings",
			validators: []validation.Validator{
				&fakeValidator{id: "pX", violation: violation("pX")},
			},
			defaults: map[string]PolicyDefaults{
				"pX": {Strict: Warn, Relaxed: Warn},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "relaxed"},
			wantWarnings: 1,
			wantErrs:     0,
		},
		{
			name: "error-mode policy with violation routes to errors",
			validators: []validation.Validator{
				&fakeValidator{id: "pX", violation: violation("pX")},
			},
			defaults: map[string]PolicyDefaults{
				"pX": {Strict: Error, Relaxed: Error},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "relaxed"},
			wantWarnings: 0,
			wantErrs:     1,
		},
		{
			name: "passing validator contributes nothing",
			validators: []validation.Validator{
				&fakeValidator{id: "pX", violation: nil},
			},
			defaults: map[string]PolicyDefaults{
				"pX": {Strict: Error, Relaxed: Error},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "strict"},
			wantWarnings: 0,
			wantErrs:     0,
		},
		{
			name: "mixed warn+error: partitioned correctly, both surface",
			validators: []validation.Validator{
				&fakeValidator{id: "pWarn", violation: violation("pWarn")},
				&fakeValidator{id: "pErr", violation: violation("pErr")},
			},
			defaults: map[string]PolicyDefaults{
				"pWarn": {Strict: Warn, Relaxed: Warn},
				"pErr":  {Strict: Error, Relaxed: Error},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "relaxed"},
			wantWarnings: 1,
			wantErrs:     1,
		},
		{
			name: "no short-circuit: error-policy violation does not stop later policies",
			validators: []validation.Validator{
				&fakeValidator{id: "pErr", violation: violation("pErr")},
				&fakeValidator{id: "pWarn", violation: violation("pWarn")},
			},
			defaults: map[string]PolicyDefaults{
				"pErr":  {Strict: Error, Relaxed: Error},
				"pWarn": {Strict: Warn, Relaxed: Warn},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "relaxed"},
			wantWarnings: 1,
			wantErrs:     1,
		},
		{
			name: "override forces error to warn",
			validators: []validation.Validator{
				&fakeValidator{id: "pErr", violation: violation("pErr")},
			},
			defaults: map[string]PolicyDefaults{
				"pErr": {Strict: Error, Relaxed: Error},
			},
			cfg: config.AdmissionPoliciesConfig{
				Mode:      "relaxed",
				Overrides: map[string]string{"pErr": "warn"},
			},
			wantWarnings: 1,
			wantErrs:     0,
		},
		{
			name: "override forces warn to error",
			validators: []validation.Validator{
				&fakeValidator{id: "pWarn", violation: violation("pWarn")},
			},
			defaults: map[string]PolicyDefaults{
				"pWarn": {Strict: Warn, Relaxed: Warn},
			},
			cfg: config.AdmissionPoliciesConfig{
				Mode:      "strict",
				Overrides: map[string]string{"pWarn": "error"},
			},
			wantWarnings: 0,
			wantErrs:     1,
		},
		{
			// ValidateRegistry would have rejected this at startup; if it
			// somehow survives to runtime we must not crash and must not
			// reject — fall back to Warn so the request still admits.
			name: "missing defaults row — defensive fallback to warn",
			validators: []validation.Validator{
				&fakeValidator{id: "pOrphan", violation: violation("pOrphan")},
			},
			defaults:     map[string]PolicyDefaults{},
			cfg:          config.AdmissionPoliciesConfig{Mode: "strict"},
			wantWarnings: 1,
			wantErrs:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warns, errs := evaluate(context.Background(), nil, nil, tt.validators, tt.defaults, tt.cfg)

			if got := len(warns); got != tt.wantWarnings {
				t.Errorf("warnings = %d, want %d (%v)", got, tt.wantWarnings, warns)
			}
			if got := len(errs); got != tt.wantErrs {
				t.Errorf("errors = %d, want %d (%v)", got, tt.wantErrs, errs)
			}
		})
	}
}

func TestEvaluateUpdate(t *testing.T) {
	tests := []struct {
		name         string
		validators   []validation.UpdateValidator
		defaults     map[string]PolicyDefaults
		cfg          config.AdmissionPoliciesConfig
		wantWarnings int
		wantErrs     int
	}{
		{
			name:         "empty registry — no-op",
			validators:   nil,
			defaults:     nil,
			wantWarnings: 0,
			wantErrs:     0,
		},
		{
			name: "error-mode policy with violation routes to errors",
			validators: []validation.UpdateValidator{
				&fakeUpdateValidator{id: "pX", violation: violation("pX")},
			},
			defaults: map[string]PolicyDefaults{
				"pX": {Strict: Error, Relaxed: Error},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "strict"},
			wantWarnings: 0,
			wantErrs:     1,
		},
		{
			name: "passing update validator contributes nothing",
			validators: []validation.UpdateValidator{
				&fakeUpdateValidator{id: "pX", violation: nil},
			},
			defaults: map[string]PolicyDefaults{
				"pX": {Strict: Error, Relaxed: Error},
			},
			cfg:          config.AdmissionPoliciesConfig{Mode: "strict"},
			wantWarnings: 0,
			wantErrs:     0,
		},
		{
			name: "override forces error to warn",
			validators: []validation.UpdateValidator{
				&fakeUpdateValidator{id: "pErr", violation: violation("pErr")},
			},
			defaults: map[string]PolicyDefaults{
				"pErr": {Strict: Error, Relaxed: Error},
			},
			cfg: config.AdmissionPoliciesConfig{
				Mode:      "strict",
				Overrides: map[string]string{"pErr": "warn"},
			},
			wantWarnings: 1,
			wantErrs:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warns, errs := evaluateUpdate(context.Background(), nil, nil, nil, tt.validators, tt.defaults, tt.cfg)

			if got := len(warns); got != tt.wantWarnings {
				t.Errorf("warnings = %d, want %d (%v)", got, tt.wantWarnings, warns)
			}
			if got := len(errs); got != tt.wantErrs {
				t.Errorf("errors = %d, want %d (%v)", got, tt.wantErrs, errs)
			}
		})
	}
}

// TestValidateRegistry covers the four invariants enforced at startup:
// every validator has a defaults row, every defaults row has a validator,
// no duplicate IDs, every override key references a known policy with a
// valid value. Each branch is its own subtest so a failure points at the
// specific invariant that broke.
//
// We exercise ValidateRegistry against the real production registries
// (validation.WekaCluster / WekaClient + wekaClusterDefaults / wekaClientDefaults),
// then construct synthetic configs via cfg.Overrides — that's the only
// dimension we can perturb without touching package globals.
func TestValidateRegistry(t *testing.T) {
	t.Run("happy path — production registry + no overrides", func(t *testing.T) {
		if err := ValidateRegistry(config.AdmissionPoliciesConfig{}); err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("happy path — production registry + valid override", func(t *testing.T) {
		// Pick any registered cluster policy ID — pulling from the live
		// defaults table avoids hard-coding a name that might be renamed.
		var anyID string
		for id := range wekaClusterDefaults {
			anyID = id
			break
		}
		if anyID == "" {
			t.Skip("no cluster policies registered")
		}
		err := ValidateRegistry(config.AdmissionPoliciesConfig{
			Overrides: map[string]string{anyID: "warn"},
		})
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("happy path — update policy IDs accepted in overrides", func(t *testing.T) {
		var anyUpdateID string
		for id := range wekaClusterUpdateDefaults {
			anyUpdateID = id
			break
		}
		if anyUpdateID == "" {
			t.Skip("no cluster update policies registered")
		}
		err := ValidateRegistry(config.AdmissionPoliciesConfig{
			Overrides: map[string]string{anyUpdateID: "warn"},
		})
		if err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})

	t.Run("override references unknown policy", func(t *testing.T) {
		err := ValidateRegistry(config.AdmissionPoliciesConfig{
			Overrides: map[string]string{"cluster_typo_does_not_exist": "warn"},
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "unknown policy") {
			t.Errorf("error should mention unknown policy, got: %v", err)
		}
	})

	t.Run("override has invalid value", func(t *testing.T) {
		var anyID string
		for id := range wekaClusterDefaults {
			anyID = id
			break
		}
		err := ValidateRegistry(config.AdmissionPoliciesConfig{
			Overrides: map[string]string{anyID: "ignore"},
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "invalid value") {
			t.Errorf("error should mention invalid value, got: %v", err)
		}
	})
}
