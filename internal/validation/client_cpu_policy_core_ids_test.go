package validation

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestClientCpuPolicyCoreIds(t *testing.T) {
	type spec = weka.WekaClientSpec
	const (
		auto        = weka.CpuPolicyAuto
		man         = weka.CpuPolicyManual
		shr         = weka.CpuPolicyShared
		dedicated   = weka.CpuPolicyDedicated
		dedicatedHT = weka.CpuPolicyDedicatedHT
	)
	ids, other := []int{1, 2}, []int{3}
	tests := []struct {
		name         string
		old, new     *spec // old nil = create
		wantRejected bool
	}{
		// create
		{"create manual without coreIds", nil, &spec{CpuPolicy: man}, true},
		{"create manual with coreIds", nil, &spec{CpuPolicy: man, CoreIds: ids}, false},
		{"create manual with both lists", nil, &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, false},
		{"create auto with coreIds", nil, &spec{CpuPolicy: auto, CoreIds: ids}, true},
		{"create empty policy with coreIds", nil, &spec{CoreIds: ids}, true},
		{"create dedicated with coreIds", nil, &spec{CpuPolicy: dedicated, CoreIds: ids}, true},
		{"create shared with coreIds", nil, &spec{CpuPolicy: shr, CoreIds: ids}, true},
		{"create shared with nonDatapath", nil, &spec{CpuPolicy: shr, NonDatapathCoreIds: ids}, false},
		{"create auto with nonDatapath", nil, &spec{CpuPolicy: auto, NonDatapathCoreIds: ids}, true},
		{"create dedicated with nonDatapath", nil, &spec{CpuPolicy: dedicated, NonDatapathCoreIds: ids}, true},
		{"create dedicated_ht with nonDatapath", nil, &spec{CpuPolicy: dedicatedHT, NonDatapathCoreIds: ids}, true},
		{"create dedicated without lists", nil, &spec{CpuPolicy: dedicated}, false},
		{"create empty spec", nil, &spec{}, false},
		// update: unrelated change on an object that already violates the create rules
		{"update unrelated field of auto+coreIds", &spec{CpuPolicy: auto, CoreIds: ids}, &spec{CpuPolicy: auto, CoreIds: ids}, false},
		{"update unrelated field of auto+nonDatapath", &spec{NonDatapathCoreIds: ids}, &spec{NonDatapathCoreIds: ids}, false},
		{"update unrelated field of manual", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: man, CoreIds: ids}, false},
		// update: policy changes without lists
		{"update auto to shared", &spec{CpuPolicy: auto}, &spec{CpuPolicy: shr}, true},
		// shared+nonDatapath -> auto clearing (allowed) -> shared: containers still hold the old nonDatapath list
		{"update shared+nonDatapath to auto clearing", &spec{CpuPolicy: shr, NonDatapathCoreIds: other}, &spec{CpuPolicy: auto}, false},
		{"update auto (cleared from shared) back to shared", &spec{CpuPolicy: auto}, &spec{CpuPolicy: shr}, true},
		{"update manual coreIds-only to shared clearing", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: shr}, true},
		{"update dedicated to dedicated_ht", &spec{CpuPolicy: dedicated}, &spec{CpuPolicy: dedicatedHT}, false},
		// update: into manual
		{"update shared to manual without coreIds", &spec{CpuPolicy: shr}, &spec{CpuPolicy: man}, true},
		{"update auto to manual with coreIds", &spec{CpuPolicy: auto}, &spec{CpuPolicy: man, CoreIds: ids}, true},
		{"update auto+coreIds to manual same lists", &spec{CpuPolicy: auto, CoreIds: ids}, &spec{CpuPolicy: man, CoreIds: ids}, true},
		// update: setting or changing lists
		{"update set coreIds on auto", &spec{CpuPolicy: auto}, &spec{CpuPolicy: auto, CoreIds: ids}, true},
		{"update set coreIds on dedicated", &spec{CpuPolicy: dedicated}, &spec{CpuPolicy: dedicated, CoreIds: ids}, true},
		{"update set nonDatapath on shared", &spec{CpuPolicy: shr}, &spec{CpuPolicy: shr, NonDatapathCoreIds: ids}, true},
		{"update set nonDatapath on dedicated_ht", &spec{CpuPolicy: dedicatedHT}, &spec{CpuPolicy: dedicatedHT, NonDatapathCoreIds: ids}, true},
		{"update change coreIds in manual", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: man, CoreIds: other}, true},
		{"update change nonDatapath in manual", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, true},
		{"update clear nonDatapath in manual", &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, &spec{CpuPolicy: man, CoreIds: ids}, true},
		{"update clear coreIds in manual", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: man}, true},
		{"update change nonDatapath in shared", &spec{CpuPolicy: shr, NonDatapathCoreIds: ids}, &spec{CpuPolicy: shr, NonDatapathCoreIds: other}, true},
		// update: clearing lists
		{"update manual to auto clearing both", &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, &spec{CpuPolicy: auto}, false},
		{"update manual to dedicated clearing both", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: dedicated}, false},
		{"update manual to dedicated_ht clearing both", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: dedicatedHT}, false},
		{"update manual to shared clearing both", &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, &spec{CpuPolicy: shr}, true},
		{"update manual to auto keeping lists", &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, &spec{CpuPolicy: auto, CoreIds: ids, NonDatapathCoreIds: other}, true},
		{"update manual to shared keeping nonDatapath", &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, &spec{CpuPolicy: shr, NonDatapathCoreIds: other}, true},
		{"update manual to auto clearing only coreIds", &spec{CpuPolicy: man, CoreIds: ids, NonDatapathCoreIds: other}, &spec{CpuPolicy: auto, NonDatapathCoreIds: other}, true},
		{"update clear coreIds of auto+coreIds", &spec{CoreIds: ids}, &spec{}, false},
		{"update auto+coreIds to dedicated clearing", &spec{CoreIds: ids}, &spec{CpuPolicy: dedicated}, false},
		{"update auto coreIds grow", &spec{CoreIds: []int{1}}, &spec{CoreIds: ids}, true},
		{"update both lists in manual", &spec{CpuPolicy: man, CoreIds: ids}, &spec{CpuPolicy: man, CoreIds: other, NonDatapathCoreIds: other}, true},
		{"update auto+coreIds to shared clearing", &spec{CoreIds: ids}, &spec{CpuPolicy: shr}, true},
	}
	v := clientCpuPolicyCoreIds{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var old runtime.Object // nil on create
			if tc.old != nil {
				old = &weka.WekaClient{Spec: *tc.old}
			}
			errs := v.ValidateUpdate(context.Background(), nil, old, &weka.WekaClient{Spec: *tc.new})
			if (len(errs) > 0) != tc.wantRejected {
				t.Fatalf("rejected=%v, want %v: %v", len(errs) > 0, tc.wantRejected, errs)
			}
		})
	}
}

func TestClientCpuPolicyCoreIdsListChangeFieldPath(t *testing.T) {
	type spec = weka.WekaClientSpec
	for want, tc := range map[string][2]spec{
		"spec.coreIds":            {{CpuPolicy: weka.CpuPolicyManual, CoreIds: []int{1}}, {CpuPolicy: weka.CpuPolicyManual, CoreIds: []int{2}}},
		"spec.nonDatapathCoreIds": {{CpuPolicy: weka.CpuPolicyShared, NonDatapathCoreIds: []int{1}}, {CpuPolicy: weka.CpuPolicyShared, NonDatapathCoreIds: []int{2}}},
	} {
		errs := clientCpuPolicyCoreIds{}.ValidateUpdate(context.Background(), nil, &weka.WekaClient{Spec: tc[0]}, &weka.WekaClient{Spec: tc[1]})
		if len(errs) != 1 || errs[0].Field != want {
			t.Errorf("want one error on %s, got %v", want, errs)
		}
	}
}
