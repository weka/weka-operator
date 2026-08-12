package validation

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterPodspecSyntax rejects WekaClusters whose scheduling-related fields
// would produce pods the API server rejects at create time (invalid
// toleration keys/enums, label syntax, topology keys, affinity). Pure spec
// math; see podspec_syntax.go for the shared checks.
type clusterPodspecSyntax struct{}

func (clusterPodspecSyntax) ID() string { return "cluster_podspec_syntax" }

func (clusterPodspecSyntax) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	spec := field.NewPath("spec")
	var errs field.ErrorList

	errs = append(errs, validateSimpleTolerations(spec.Child("tolerations"), wc.Spec.Tolerations)...)
	errs = append(errs, validateRawTolerations(spec.Child("rawTolerations"), wc.Spec.RawTolerations)...)
	errs = append(errs, validateLabelMap(spec.Child("nodeSelector"), wc.Spec.NodeSelector)...)

	// RoleNodeSelector and RoleAnnotations have six roles (compute, drive, s3,
	// nfs, smbw, dataServices); RoleAffinity below has only five — there is no
	// dataServices field on that struct.
	roleNodeSelectors := []struct {
		role string
		sel  *map[string]string
	}{
		{"compute", wc.Spec.RoleNodeSelector.Compute},
		{"drive", wc.Spec.RoleNodeSelector.Drive},
		{"s3", wc.Spec.RoleNodeSelector.S3},
		{"nfs", wc.Spec.RoleNodeSelector.Nfs},
		{"smbw", wc.Spec.RoleNodeSelector.Smbw},
		{"dataServices", wc.Spec.RoleNodeSelector.DataServices},
	}
	for _, r := range roleNodeSelectors {
		if r.sel != nil {
			errs = append(errs, validateLabelMap(spec.Child("roleNodeSelector", r.role), *r.sel)...)
		}
	}

	roleAnnotations := []struct {
		role string
		ann  *map[string]string
	}{
		{"compute", wc.Spec.RoleAnnotations.Compute},
		{"drive", wc.Spec.RoleAnnotations.Drive},
		{"s3", wc.Spec.RoleAnnotations.S3},
		{"nfs", wc.Spec.RoleAnnotations.Nfs},
		{"smbw", wc.Spec.RoleAnnotations.Smbw},
		{"dataServices", wc.Spec.RoleAnnotations.DataServices},
	}
	for _, r := range roleAnnotations {
		if r.ann != nil {
			errs = append(errs, validateAnnotationMap(spec.Child("roleAnnotations", r.role), *r.ann)...)
		}
	}

	if fd := wc.Spec.FailureDomain; fd != nil {
		// mirror getDefaultRoleTopologySpreadConstraints precedence: label
		// wins over compositeLabels; skew is used only with label
		if fd.Label != nil {
			if *fd.Label == "" {
				errs = append(errs, field.Required(spec.Child("failureDomain", "label"), "failureDomain label may not be empty when set"))
			} else {
				errs = append(errs, validateTopologyKey(spec.Child("failureDomain", "label"), *fd.Label)...)
			}
			// skew becomes the generated spread constraint's maxSkew (must be > 0)
			if fd.Skew != nil && *fd.Skew <= 0 {
				errs = append(errs, field.Invalid(spec.Child("failureDomain", "skew"), *fd.Skew, "must be greater than zero"))
			}
		} else {
			for i, l := range fd.CompositeLabels {
				p := spec.Child("failureDomain", "compositeLabels").Index(i)
				if l == "" {
					errs = append(errs, field.Required(p, "failureDomain compositeLabels entries may not be empty"))
				} else {
					errs = append(errs, validateTopologyKey(p, l)...)
				}
			}
		}
	}

	if pc := wc.Spec.PodConfig; pc != nil {
		errs = append(errs, validateRawAffinity(spec.Child("podConfig", "affinity"), pc.Affinity)...)
		if ra := pc.RoleAffinity; ra != nil {
			roleAffinities := []struct {
				role string
				raw  *runtime.RawExtension
			}{
				{"compute", ra.Compute},
				{"drive", ra.Drive},
				{"s3", ra.S3},
				{"nfs", ra.Nfs},
				{"smbw", ra.Smbw},
			}
			for _, r := range roleAffinities {
				if r.raw != nil {
					errs = append(errs, validateRawAffinity(spec.Child("podConfig", "roleAffinity", r.role), r.raw)...)
				}
			}
		}

		errs = append(errs, validateRawTopologySpreadConstraints(spec.Child("podConfig", "topologySpreadConstraints"), pc.TopologySpreadConstraints)...)
		if rtsc := pc.RoleTopologySpreadConstraints; rtsc != nil {
			// RoleTopologySpreadConstraints has five roles (no dataServices field),
			// same set as RoleAffinity above.
			roleTopologySpreadConstraints := []struct {
				role string
				raw  *runtime.RawExtension
			}{
				{"compute", rtsc.Compute},
				{"drive", rtsc.Drive},
				{"s3", rtsc.S3},
				{"nfs", rtsc.Nfs},
				{"smbw", rtsc.Smbw},
			}
			for _, r := range roleTopologySpreadConstraints {
				if r.raw != nil {
					errs = append(errs, validateRawTopologySpreadConstraints(spec.Child("podConfig", "roleTopologySpreadConstraints", r.role), r.raw)...)
				}
			}
		}
	}
	return errs
}
