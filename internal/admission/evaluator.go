package admission

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/validation"
)

// evaluate runs every registered validator and partitions output by each
// policy's effective Mode. All validators run unconditionally so one apply
// surfaces every violation at once.
func evaluate(
	ctx context.Context,
	c client.Client,
	obj runtime.Object,
	validators []validation.Validator,
	defaults map[string]PolicyDefaults,
	cfg config.AdmissionPoliciesConfig,
) (admission.Warnings, error) {
	if len(validators) == 0 {
		return nil, nil
	}

	var warnings admission.Warnings
	var errs field.ErrorList

	for _, v := range validators {
		id := v.ID()
		def, ok := defaults[id]
		if !ok {
			// ValidateRegistry() rejects this at startup; fall back to warn so
			// a live request never rejects on a programmer error.
			def = PolicyDefaults{Strict: Warn, Relaxed: Warn}
		}
		m := modeFor(cfg.Mode, cfg.Overrides[id], def)

		for _, violation := range v.Validate(ctx, c, obj) {
			switch m {
			case Warn:
				warnings = append(warnings, violation.Error())
			case Error:
				errs = append(errs, violation)
			}
		}
	}

	return warnings, errs.ToAggregate()
}

// ValidateRegistry checks that every registered validator has a defaults
// row, every defaults row has a registered validator, and every override
// key in cfg references a known policy with a valid value. Run once at
// startup; a mismatch is a programmer/config error and should fail fast.
func ValidateRegistry(cfg config.AdmissionPoliciesConfig) error {
	var errs []string

	check := func(kind string, validators []validation.Validator, defaults map[string]PolicyDefaults) {
		ids := map[string]bool{}
		for _, v := range validators {
			id := v.ID()
			if _, seen := ids[id]; seen {
				errs = append(errs, fmt.Sprintf("%s: duplicate validator ID %q", kind, id))
			}
			ids[id] = true
			if _, ok := defaults[id]; !ok {
				errs = append(errs, fmt.Sprintf("%s: validator %q has no entry in defaults table", kind, id))
			}
		}
		for id := range defaults {
			if _, ok := ids[id]; !ok {
				errs = append(errs, fmt.Sprintf("%s: defaults table has entry %q but no validator is registered with that ID", kind, id))
			}
		}
	}
	check("WekaCluster", validation.WekaCluster, wekaClusterDefaults)
	check("WekaClient", validation.WekaClient, wekaClientDefaults)

	known := map[string]bool{}
	for id := range wekaClusterDefaults {
		known[id] = true
	}
	for id := range wekaClientDefaults {
		known[id] = true
	}
	for id, val := range cfg.Overrides {
		if !known[id] {
			errs = append(errs, fmt.Sprintf("admissionPolicies.policies: unknown policy %q (not registered)", id))
		}
		switch strings.ToLower(val) {
		case "default", "warn", "error":
		default:
			errs = append(errs, fmt.Sprintf("admissionPolicies.policies[%q]: invalid value %q (expected default|warn|error)", id, val))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("admission registry validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
