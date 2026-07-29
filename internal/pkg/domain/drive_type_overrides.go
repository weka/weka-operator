package domain

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/consts"
)

const (
	driveTypeTLC = "TLC"
	driveTypeQLC = "QLC"
)

// ApplyDriveTypeOverrides applies the override rules to drives, returning the updated slice, the
// number of drives whose Type changed, and the indexes of rules that matched no drive (dead
// rules). Rules are evaluated in order; the first match on a drive wins its Type, but a rule
// shadowed by an earlier match on every drive it applies to still counts as matched — reporting
// it as unmatched would be a false positive. Model matches case-insensitively (trimmed);
// CapacityGiB matches exactly; when both are set on a rule, both must match. Rules whose Type
// isn't exactly "TLC" or "QLC" are ignored.
func ApplyDriveTypeOverrides(drives []SharedDriveInfo, rules []v1alpha1.DriveTypeOverrideRule) (out []SharedDriveInfo, changed int, unmatchedRules []int) {
	out = make([]SharedDriveInfo, len(drives))
	copy(out, drives)

	matched := make([]bool, len(rules))

	for i := range out {
		drive := out[i]
		won := false
		for ruleIdx, rule := range rules {
			// Rules with an invalid Type are ignored: they never match, so they are never
			// marked matched and end up reported in unmatchedRules below.
			if rule.Type != driveTypeTLC && rule.Type != driveTypeQLC {
				continue
			}
			if !driveTypeOverrideRuleMatches(rule, drive) {
				continue
			}
			// Counts as matched even if an earlier rule already won this drive (see doc comment).
			matched[ruleIdx] = true
			if won {
				continue
			}
			won = true
			if drive.Type != rule.Type {
				out[i].Type = rule.Type
				changed++
			}
		}
	}

	for i, ok := range matched {
		if !ok {
			unmatchedRules = append(unmatchedRules, i)
		}
	}

	return out, changed, unmatchedRules
}

// driveTypeOverrideRuleMatches reports whether rule matches drive. At least one of Model or
// CapacityGiB must be set on the rule (enforced by CRD validation); when both are set, both
// must match.
func driveTypeOverrideRuleMatches(rule v1alpha1.DriveTypeOverrideRule, drive SharedDriveInfo) bool {
	// Trimmed once and reused below: a whitespace-only Model must count as "no constraint", not
	// as matching a drive with no recorded Model (annotated before the Model field existed).
	ruleModel := strings.TrimSpace(rule.Model)

	if ruleModel != "" {
		if !strings.EqualFold(ruleModel, strings.TrimSpace(drive.Model)) {
			return false
		}
	}
	if rule.CapacityGiB != 0 {
		if rule.CapacityGiB != drive.CapacityGiB {
			return false
		}
	}
	// A rule that constrains nothing matches nothing, rather than everything.
	return ruleModel != "" || rule.CapacityGiB != 0
}

// ReadDriveTypeOverrides reads the override rules from the node annotation.
// Returns nil, nil when the annotation is absent or empty.
func ReadDriveTypeOverrides(node *corev1.Node) ([]v1alpha1.DriveTypeOverrideRule, error) {
	raw, ok := node.Annotations[consts.AnnotationDriveTypeOverrides]
	if !ok || raw == "" {
		return nil, nil
	}
	var rules []v1alpha1.DriveTypeOverrideRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("failed to parse drive-type-overrides: %w", err)
	}
	return rules, nil
}

// WriteDriveTypeOverrides stores the rules on the node. Empty or nil rules delete the annotation.
func WriteDriveTypeOverrides(node *corev1.Node, rules []v1alpha1.DriveTypeOverrideRule) error {
	if len(rules) == 0 {
		delete(node.Annotations, consts.AnnotationDriveTypeOverrides)
		return nil
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("failed to marshal drive-type-overrides: %w", err)
	}
	node.Annotations[consts.AnnotationDriveTypeOverrides] = string(rulesJSON)
	return nil
}

// MergeSharedDriveInfo merges a freshly reported drive into the existing annotated one.
// Incoming values win per field, EXCEPT that an empty/zero incoming value preserves the
// existing one. This prevents an agent image that does not report Model (or a failed model
// lookup) from erasing a persisted Model and silently disarming model-based override rules.
func MergeSharedDriveInfo(existing, incoming SharedDriveInfo) SharedDriveInfo {
	merged := incoming
	if merged.PhysicalUUID == "" {
		merged.PhysicalUUID = existing.PhysicalUUID
	}
	if merged.Serial == "" {
		merged.Serial = existing.Serial
	}
	if merged.CapacityGiB == 0 {
		merged.CapacityGiB = existing.CapacityGiB
	}
	if merged.Type == "" {
		merged.Type = existing.Type
	}
	if merged.Model == "" {
		merged.Model = existing.Model
	}
	return merged
}
