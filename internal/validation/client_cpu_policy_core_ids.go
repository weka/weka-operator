package validation

import (
	"context"
	"slices"

	wekav1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientCpuPolicyCoreIds ties spec.coreIds and spec.nonDatapathCoreIds to spec.cpuPolicy. Core lists
// are copied to client containers only at creation, so on update any list change is rejected except
// clearing both for a policy that ignores them. Also run on create, with a nil oldObj.
type clientCpuPolicyCoreIds struct{}

func (clientCpuPolicyCoreIds) ID() string { return "client_cpu_policy_core_ids" }

// effectivePolicy treats an empty policy as auto, its kubebuilder default.
func effectivePolicy(spec *wekav1alpha1.WekaClientSpec) wekav1alpha1.CpuPolicy {
	if spec.CpuPolicy == "" {
		return wekav1alpha1.CpuPolicyAuto
	}
	return spec.CpuPolicy
}

func (clientCpuPolicyCoreIds) ValidateUpdate(_ context.Context, _ client.Client, oldObj, newObj runtime.Object) field.ErrorList {
	newWC, ok := newObj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	n := &newWC.Spec
	p := effectivePolicy(n)
	oldWC, isUpdate := oldObj.(*wekav1alpha1.WekaClient)
	if !isUpdate {
		return clientCpuPolicyRules(n, p)
	}
	o := &oldWC.Spec
	oldP := effectivePolicy(o)

	coresChanged := !slices.Equal(o.CoreIds, n.CoreIds)
	nonDpChanged := !slices.Equal(o.NonDatapathCoreIds, n.NonDatapathCoreIds)
	ignoresLists := p == wekav1alpha1.CpuPolicyAuto || p == wekav1alpha1.CpuPolicyDedicated || p == wekav1alpha1.CpuPolicyDedicatedHT
	cleared := len(n.CoreIds) == 0 && len(n.NonDatapathCoreIds) == 0 && ignoresLists

	var errs field.ErrorList
	switch {
	// existing containers keep the lists from their creation, and manual / shared would apply them again
	case (p == wekav1alpha1.CpuPolicyManual || p == wekav1alpha1.CpuPolicyShared) && oldP != p:
		errs = append(errs, field.Forbidden(field.NewPath("spec", "cpuPolicy"),
			"existing client containers keep the core lists they were created with; recreate the WekaClient to use cpuPolicy "+string(p)))
	case (coresChanged || nonDpChanged) && !cleared:
		path := field.NewPath("spec", "coreIds")
		if !coresChanged {
			path = field.NewPath("spec", "nonDatapathCoreIds")
		}
		errs = append(errs, field.Forbidden(path,
			"changes to coreIds or nonDatapathCoreIds are not applied to existing client containers; recreate the WekaClient"))
	case oldP != p:
		errs = clientCpuPolicyRules(n, p)
	}
	return errs
}

func clientCpuPolicyRules(n *wekav1alpha1.WekaClientSpec, p wekav1alpha1.CpuPolicy) field.ErrorList {
	var errs field.ErrorList
	manual := p == wekav1alpha1.CpuPolicyManual
	if manual && len(n.CoreIds) == 0 {
		errs = append(errs, field.Required(field.NewPath("spec", "coreIds"), "cpuPolicy manual requires coreIds"))
	}
	if !manual && len(n.CoreIds) > 0 {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "coreIds"), "coreIds is only valid with cpuPolicy manual"))
	}
	if !manual && p != wekav1alpha1.CpuPolicyShared && len(n.NonDatapathCoreIds) > 0 {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "nonDatapathCoreIds"), "nonDatapathCoreIds is only valid with cpuPolicy manual or shared"))
	}
	return errs
}
