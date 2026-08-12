package validation

// Shared syntax checks for CR fields that are copied verbatim into pod /
// deployment specs. The API server applies these rules only when the Pod is
// created (Gate 2); running them here surfaces the rejection at CR apply.
// Pure spec math — no cluster reads.

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// validateSimpleTolerations checks the []string convenience form: each entry
// must be a bare taint key. ExpandTolerations puts the whole string into
// Toleration.Key, so anything that is not a qualified name produces a pod the
// API server rejects.
func validateSimpleTolerations(fldPath *field.Path, tolerations []string) field.ErrorList {
	var errs field.ErrorList
	for i, s := range tolerations {
		// "" expands to a valid tolerate-all toleration
		if s == "" {
			continue
		}
		if msgs := utilvalidation.IsQualifiedName(s); len(msgs) > 0 {
			detail := fmt.Sprintf("must be a bare taint key (e.g. %q): %s", "weka.io/dedicated", strings.Join(msgs, "; "))
			if strings.ContainsAny(s, ":=") {
				detail += ". This field takes only the taint key; to specify an effect or value, use rawTolerations instead"
			}
			errs = append(errs, field.Invalid(fldPath.Index(i), s, detail))
		}
	}
	return errs
}

var (
	validTolerationOperators = map[corev1.TolerationOperator]bool{
		"": true, corev1.TolerationOpEqual: true, corev1.TolerationOpExists: true,
	}
	validTaintEffects = map[corev1.TaintEffect]bool{
		"": true, corev1.TaintEffectNoSchedule: true, corev1.TaintEffectPreferNoSchedule: true, corev1.TaintEffectNoExecute: true,
	}
)

// validateRawTolerations mirrors the syntax rules the API server applies to
// pod .spec.tolerations (upstream pkg/apis/core/validation is not importable).
func validateRawTolerations(fldPath *field.Path, tolerations []corev1.Toleration) field.ErrorList {
	var errs field.ErrorList
	for i, t := range tolerations {
		p := fldPath.Index(i)
		if t.Key != "" {
			for _, msg := range utilvalidation.IsQualifiedName(t.Key) {
				errs = append(errs, field.Invalid(p.Child("key"), t.Key, msg))
			}
		} else if t.Operator != corev1.TolerationOpExists {
			errs = append(errs, field.Invalid(p.Child("operator"), t.Operator, "operator must be Exists when key is empty"))
		}
		if !validTolerationOperators[t.Operator] {
			errs = append(errs, field.NotSupported(p.Child("operator"), t.Operator, []string{string(corev1.TolerationOpEqual), string(corev1.TolerationOpExists)}))
		}
		if t.Operator == corev1.TolerationOpExists && t.Value != "" {
			errs = append(errs, field.Invalid(p.Child("value"), t.Value, "value must be empty when operator is Exists"))
		}
		// upstream checks value syntax only under Equal
		if t.Operator == "" || t.Operator == corev1.TolerationOpEqual {
			for _, msg := range utilvalidation.IsValidLabelValue(t.Value) {
				errs = append(errs, field.Invalid(p.Child("value"), t.Value, msg))
			}
		}
		if !validTaintEffects[t.Effect] {
			errs = append(errs, field.NotSupported(p.Child("effect"), t.Effect, []string{string(corev1.TaintEffectNoSchedule), string(corev1.TaintEffectPreferNoSchedule), string(corev1.TaintEffectNoExecute)}))
		}
		if t.TolerationSeconds != nil && t.Effect != corev1.TaintEffectNoExecute {
			errs = append(errs, field.Invalid(p.Child("effect"), t.Effect, "effect must be NoExecute when tolerationSeconds is set"))
		}
	}
	return errs
}

// validateLabelMap checks label-syntax maps (nodeSelector, csi labels) via
// the same function the API server uses.
func validateLabelMap(fldPath *field.Path, m map[string]string) field.ErrorList {
	return metav1validation.ValidateLabels(m, fldPath)
}

// validateAnnotationMap checks annotation keys + total size (values are free-form).
func validateAnnotationMap(fldPath *field.Path, m map[string]string) field.ErrorList {
	return apivalidation.ValidateAnnotations(m, fldPath)
}

// validateTopologyKey checks a node-label key used as a topology key
// (failureDomain labels, pod-affinity topologyKey). Empty is allowed —
// presence rules stay with the caller.
func validateTopologyKey(fldPath *field.Path, key string) field.ErrorList {
	if key == "" {
		return nil
	}
	var errs field.ErrorList
	for _, msg := range utilvalidation.IsQualifiedName(key) {
		errs = append(errs, field.Invalid(fldPath, key, msg))
	}
	return errs
}

// validateAffinity mirrors the syntax rules the API server applies to pod
// .spec.affinity (upstream pkg/apis/core/validation is not importable).
// Not checked (rare, still caught at pod create): namespaces name syntax,
// matchLabelKeys/mismatchLabelKeys.
func validateAffinity(fldPath *field.Path, aff *corev1.Affinity) field.ErrorList {
	if aff == nil {
		return nil
	}
	var errs field.ErrorList

	checkTerm := func(p *field.Path, term corev1.NodeSelectorTerm, required bool) {
		for j, expr := range term.MatchExpressions {
			ep := p.Child("matchExpressions").Index(j)
			for _, msg := range utilvalidation.IsQualifiedName(expr.Key) {
				errs = append(errs, field.Invalid(ep.Child("key"), expr.Key, msg))
			}
			switch expr.Operator {
			case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
				if len(expr.Values) == 0 {
					errs = append(errs, field.Required(ep.Child("values"), "must be specified when operator is In or NotIn"))
				}
			case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist:
				if len(expr.Values) > 0 {
					errs = append(errs, field.Forbidden(ep.Child("values"), "may not be specified when operator is Exists or DoesNotExist"))
				}
			case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
				if len(expr.Values) != 1 {
					errs = append(errs, field.Required(ep.Child("values"), "must be a single value when operator is Gt or Lt"))
				}
			default:
				errs = append(errs, field.NotSupported(ep.Child("operator"), expr.Operator, []string{"In", "NotIn", "Exists", "DoesNotExist", "Gt", "Lt"}))
			}
			// upstream checks value syntax in required terms only
			if required {
				for vi, v := range expr.Values {
					for _, msg := range utilvalidation.IsValidLabelValue(v) {
						errs = append(errs, field.Invalid(ep.Child("values").Index(vi), v, msg))
					}
				}
			}
		}
		for j, f := range term.MatchFields {
			fp := p.Child("matchFields").Index(j)
			if f.Operator != corev1.NodeSelectorOpIn && f.Operator != corev1.NodeSelectorOpNotIn {
				errs = append(errs, field.Invalid(fp.Child("operator"), f.Operator, "not a valid selector operator"))
			} else if len(f.Values) != 1 {
				errs = append(errs, field.Required(fp.Child("values"), "must be only one value when operator is In or NotIn for node field selector"))
			}
			if f.Key != "metadata.name" {
				errs = append(errs, field.Invalid(fp.Child("key"), f.Key, "not a valid field selector key"))
			} else {
				for vi, v := range f.Values {
					for _, msg := range utilvalidation.IsDNS1123Subdomain(v) {
						errs = append(errs, field.Invalid(fp.Child("values").Index(vi), v, msg))
					}
				}
			}
		}
	}
	if na := aff.NodeAffinity; na != nil {
		if req := na.RequiredDuringSchedulingIgnoredDuringExecution; req != nil {
			p := fldPath.Child("nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
			if len(req.NodeSelectorTerms) == 0 {
				errs = append(errs, field.Required(p, "must have at least one node selector term"))
			}
			for i, term := range req.NodeSelectorTerms {
				checkTerm(p.Index(i), term, true)
			}
		}
		for i, pref := range na.PreferredDuringSchedulingIgnoredDuringExecution {
			pp := fldPath.Child("nodeAffinity", "preferredDuringSchedulingIgnoredDuringExecution").Index(i)
			if pref.Weight <= 0 || pref.Weight > 100 {
				errs = append(errs, field.Invalid(pp.Child("weight"), pref.Weight, "must be in the range 1-100"))
			}
			checkTerm(pp.Child("preference"), pref.Preference, false)
		}
	}

	checkPodTerm := func(tp *field.Path, term corev1.PodAffinityTerm) {
		errs = append(errs, metav1validation.ValidateLabelSelector(term.LabelSelector, metav1validation.LabelSelectorValidationOptions{}, tp.Child("labelSelector"))...)
		errs = append(errs, metav1validation.ValidateLabelSelector(term.NamespaceSelector, metav1validation.LabelSelectorValidationOptions{}, tp.Child("namespaceSelector"))...)
		if term.TopologyKey == "" {
			errs = append(errs, field.Required(tp.Child("topologyKey"), "pod affinity terms require a topologyKey"))
		} else {
			errs = append(errs, validateTopologyKey(tp.Child("topologyKey"), term.TopologyKey)...)
		}
	}
	checkPodAffinityLists := func(base *field.Path, required []corev1.PodAffinityTerm, preferred []corev1.WeightedPodAffinityTerm) {
		for i, term := range required {
			checkPodTerm(base.Child("requiredDuringSchedulingIgnoredDuringExecution").Index(i), term)
		}
		for i, w := range preferred {
			wp := base.Child("preferredDuringSchedulingIgnoredDuringExecution").Index(i)
			if w.Weight <= 0 || w.Weight > 100 {
				errs = append(errs, field.Invalid(wp.Child("weight"), w.Weight, "must be in the range 1-100"))
			}
			checkPodTerm(wp.Child("podAffinityTerm"), w.PodAffinityTerm)
		}
	}
	if pa := aff.PodAffinity; pa != nil {
		checkPodAffinityLists(fldPath.Child("podAffinity"), pa.RequiredDuringSchedulingIgnoredDuringExecution, pa.PreferredDuringSchedulingIgnoredDuringExecution)
	}
	if paa := aff.PodAntiAffinity; paa != nil {
		checkPodAffinityLists(fldPath.Child("podAntiAffinity"), paa.RequiredDuringSchedulingIgnoredDuringExecution, paa.PreferredDuringSchedulingIgnoredDuringExecution)
	}
	return errs
}

// validateRawAffinity checks a RawExtension that the reconciler will
// unmarshal into v1.Affinity (WekaCluster podConfig.affinity/roleAffinity):
// it must unmarshal cleanly, then passes validateAffinity.
func validateRawAffinity(fldPath *field.Path, raw *runtime.RawExtension) field.ErrorList {
	if raw == nil || len(raw.Raw) == 0 {
		return nil
	}
	var aff corev1.Affinity
	if err := json.Unmarshal(raw.Raw, &aff); err != nil {
		return field.ErrorList{field.Invalid(fldPath, string(raw.Raw), fmt.Sprintf("does not unmarshal into a v1.Affinity: %v", err))}
	}
	return validateAffinity(fldPath, &aff)
}

// validateTopologySpreadConstraints mirrors the syntax rules the API server
// applies to pod .spec.topologySpreadConstraints. topologyKey only needs to
// be non-empty (qualified-name would be stricter than upstream). Not
// checked (rare, still caught at pod create): minDomains, duplicate pairs,
// node inclusion policies, matchLabelKeys.
func validateTopologySpreadConstraints(fldPath *field.Path, constraints []corev1.TopologySpreadConstraint) field.ErrorList {
	var errs field.ErrorList
	for i, c := range constraints {
		p := fldPath.Index(i)
		if c.MaxSkew <= 0 {
			errs = append(errs, field.Invalid(p.Child("maxSkew"), c.MaxSkew, "must be greater than zero"))
		}
		if c.TopologyKey == "" {
			errs = append(errs, field.Required(p.Child("topologyKey"), "topologySpreadConstraints entries require a topologyKey"))
		}
		if c.WhenUnsatisfiable != corev1.DoNotSchedule && c.WhenUnsatisfiable != corev1.ScheduleAnyway {
			errs = append(errs, field.NotSupported(p.Child("whenUnsatisfiable"), c.WhenUnsatisfiable, []string{string(corev1.DoNotSchedule), string(corev1.ScheduleAnyway)}))
		}
		errs = append(errs, metav1validation.ValidateLabelSelector(c.LabelSelector, metav1validation.LabelSelectorValidationOptions{}, p.Child("labelSelector"))...)
	}
	return errs
}

// validateRawTopologySpreadConstraints checks a RawExtension that the
// reconciler will unmarshal into []v1.TopologySpreadConstraint (WekaCluster
// podConfig.topologySpreadConstraints/roleTopologySpreadConstraints): it must
// unmarshal cleanly, mirroring unmarshalTopologySpreadConstraints in
// wekacluster_types.go, then passes validateTopologySpreadConstraints.
func validateRawTopologySpreadConstraints(fldPath *field.Path, raw *runtime.RawExtension) field.ErrorList {
	if raw == nil || len(raw.Raw) == 0 {
		return nil
	}
	var constraints []corev1.TopologySpreadConstraint
	if err := json.Unmarshal(raw.Raw, &constraints); err != nil {
		return field.ErrorList{field.Invalid(fldPath, string(raw.Raw), fmt.Sprintf("does not unmarshal into []v1.TopologySpreadConstraint: %v", err))}
	}
	return validateTopologySpreadConstraints(fldPath, constraints)
}
