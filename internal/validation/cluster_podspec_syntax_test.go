package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestClusterPodspecSyntax(t *testing.T) {
	v := clusterPodspecSyntax{}
	base := func() *weka.WekaCluster {
		return &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	}

	if errs := v.Validate(context.Background(), nil, base()); len(errs) != 0 {
		t.Fatalf("empty spec should pass: %v", errs)
	}

	badTol := base()
	badTol.Spec.Tolerations = []string{"scitix.ai/nodecheck:NoSchedule"}
	if errs := v.Validate(context.Background(), nil, badTol); len(errs) != 1 {
		t.Fatalf("bad string toleration: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.tolerations") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badRawTol := base()
	badRawTol.Spec.RawTolerations = []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Effect: "NoSchedul"}}
	if errs := v.Validate(context.Background(), nil, badRawTol); len(errs) != 1 {
		t.Fatalf("bad rawToleration effect: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.rawTolerations") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badSel := base()
	badSel.Spec.NodeSelector = map[string]string{"bad key!": "x"}
	if errs := v.Validate(context.Background(), nil, badSel); len(errs) != 1 {
		t.Fatalf("bad nodeSelector: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.nodeSelector") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badRole := base()
	m := map[string]string{"k": "bad value!"}
	badRole.Spec.RoleNodeSelector.Compute = &m
	if errs := v.Validate(context.Background(), nil, badRole); len(errs) != 1 {
		t.Fatalf("bad roleNodeSelector.compute: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.roleNodeSelector.compute") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badRoleAnn := base()
	ann := map[string]string{"bad key!": "x"}
	badRoleAnn.Spec.RoleAnnotations.Compute = &ann
	if errs := v.Validate(context.Background(), nil, badRoleAnn); len(errs) != 1 {
		t.Fatalf("bad roleAnnotations.compute: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.roleAnnotations.compute") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badFD := base()
	label := "bad key!"
	badFD.Spec.FailureDomain = &weka.FailureDomain{Label: &label}
	if errs := v.Validate(context.Background(), nil, badFD); len(errs) != 1 {
		t.Fatalf("bad failureDomain.label: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.failureDomain.label") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badComposite := base()
	badComposite.Spec.FailureDomain = &weka.FailureDomain{CompositeLabels: []string{"bad key!"}}
	if errs := v.Validate(context.Background(), nil, badComposite); len(errs) != 1 {
		t.Fatalf("bad failureDomain.compositeLabels: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.failureDomain.compositeLabels") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	emptyLabel := base()
	empty := ""
	emptyLabel.Spec.FailureDomain = &weka.FailureDomain{Label: &empty}
	if errs := v.Validate(context.Background(), nil, emptyLabel); len(errs) != 1 {
		t.Fatalf("empty failureDomain.label: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.failureDomain.label") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	emptyComposite := base()
	emptyComposite.Spec.FailureDomain = &weka.FailureDomain{CompositeLabels: []string{""}}
	if errs := v.Validate(context.Background(), nil, emptyComposite); len(errs) != 1 {
		t.Fatalf("empty failureDomain.compositeLabels entry: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.failureDomain.compositeLabels") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badAffinity := base()
	badAffinity.Spec.PodConfig = &weka.PodConfiguration{Affinity: &runtime.RawExtension{Raw: []byte("not json")}}
	if errs := v.Validate(context.Background(), nil, badAffinity); len(errs) != 1 {
		t.Fatalf("bad podConfig.affinity: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.podConfig.affinity") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badRoleAffinity := base()
	badRoleAffinity.Spec.PodConfig = &weka.PodConfiguration{
		RoleAffinity: &weka.RoleAffinity{
			Drive: &runtime.RawExtension{Raw: []byte(`{"nodeAffinity": 42}`)},
		},
	}
	if errs := v.Validate(context.Background(), nil, badRoleAffinity); len(errs) != 1 {
		t.Fatalf("bad podConfig.roleAffinity.drive: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.podConfig.roleAffinity.drive") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badTopo := base()
	badTopo.Spec.PodConfig = &weka.PodConfiguration{TopologySpreadConstraints: &runtime.RawExtension{Raw: []byte(`[{"topologyKey":"kubernetes.io/hostname","maxSkew":0,"whenUnsatisfiable":"DoNotSchedule"}]`)}}
	if errs := v.Validate(context.Background(), nil, badTopo); len(errs) != 1 {
		t.Fatalf("bad podConfig.topologySpreadConstraints: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.podConfig.topologySpreadConstraints[0].maxSkew") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badRoleTopo := base()
	badRoleTopo.Spec.PodConfig = &weka.PodConfiguration{
		RoleTopologySpreadConstraints: &weka.RoleTopologySpreadConstraints{
			Compute: &runtime.RawExtension{Raw: []byte(`[{"topologyKey":"","maxSkew":1,"whenUnsatisfiable":"DoNotSchedule"}]`)},
		},
	}
	if errs := v.Validate(context.Background(), nil, badRoleTopo); len(errs) != 1 {
		t.Fatalf("bad roleTopologySpreadConstraints.compute: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.podConfig.roleTopologySpreadConstraints.compute[0].topologyKey") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	zone := "topology.kubernetes.io/zone"
	zero := 0
	one := 1

	badSkew := base()
	badSkew.Spec.FailureDomain = &weka.FailureDomain{Label: &zone, Skew: &zero}
	if errs := v.Validate(context.Background(), nil, badSkew); len(errs) != 1 {
		t.Fatalf("failureDomain.skew=0 with label: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.failureDomain.skew") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	goodSkew := base()
	goodSkew.Spec.FailureDomain = &weka.FailureDomain{Label: &zone, Skew: &one}
	if errs := v.Validate(context.Background(), nil, goodSkew); len(errs) != 0 {
		t.Fatalf("failureDomain.skew=1 with label rejected: %v", errs)
	}

	// dead fields are skipped, mirroring factory precedence:
	// skew without label is unused; compositeLabels lose to label
	deadSkew := base()
	deadSkew.Spec.FailureDomain = &weka.FailureDomain{Skew: &zero}
	if errs := v.Validate(context.Background(), nil, deadSkew); len(errs) != 0 {
		t.Fatalf("unused skew=0 (no label) rejected: %v", errs)
	}
	deadComposite := base()
	deadComposite.Spec.FailureDomain = &weka.FailureDomain{Label: &zone, CompositeLabels: []string{"bad key!"}}
	if errs := v.Validate(context.Background(), nil, deadComposite); len(errs) != 0 {
		t.Fatalf("unused compositeLabels (label set) rejected: %v", errs)
	}
}

// One case per role field, catching swapped wiring in the role tables.
func TestClusterPodspecSyntaxRoleWiring(t *testing.T) {
	v := clusterPodspecSyntax{}
	badMap := map[string]string{"bad key!": "x"}
	badRaw := &runtime.RawExtension{Raw: []byte("not json")}

	sixRoles := []string{"compute", "drive", "s3", "nfs", "smbw", "dataServices"}
	fiveRoles := []string{"compute", "drive", "s3", "nfs", "smbw"}

	set := func(c *weka.WekaCluster, family, role string) {
		switch family {
		case "roleNodeSelector":
			m := badMap
			switch role {
			case "compute":
				c.Spec.RoleNodeSelector.Compute = &m
			case "drive":
				c.Spec.RoleNodeSelector.Drive = &m
			case "s3":
				c.Spec.RoleNodeSelector.S3 = &m
			case "nfs":
				c.Spec.RoleNodeSelector.Nfs = &m
			case "smbw":
				c.Spec.RoleNodeSelector.Smbw = &m
			case "dataServices":
				c.Spec.RoleNodeSelector.DataServices = &m
			}
		case "roleAnnotations":
			m := badMap
			switch role {
			case "compute":
				c.Spec.RoleAnnotations.Compute = &m
			case "drive":
				c.Spec.RoleAnnotations.Drive = &m
			case "s3":
				c.Spec.RoleAnnotations.S3 = &m
			case "nfs":
				c.Spec.RoleAnnotations.Nfs = &m
			case "smbw":
				c.Spec.RoleAnnotations.Smbw = &m
			case "dataServices":
				c.Spec.RoleAnnotations.DataServices = &m
			}
		case "podConfig.roleAffinity":
			ra := &weka.RoleAffinity{}
			switch role {
			case "compute":
				ra.Compute = badRaw
			case "drive":
				ra.Drive = badRaw
			case "s3":
				ra.S3 = badRaw
			case "nfs":
				ra.Nfs = badRaw
			case "smbw":
				ra.Smbw = badRaw
			}
			c.Spec.PodConfig = &weka.PodConfiguration{RoleAffinity: ra}
		case "podConfig.roleTopologySpreadConstraints":
			rt := &weka.RoleTopologySpreadConstraints{}
			switch role {
			case "compute":
				rt.Compute = badRaw
			case "drive":
				rt.Drive = badRaw
			case "s3":
				rt.S3 = badRaw
			case "nfs":
				rt.Nfs = badRaw
			case "smbw":
				rt.Smbw = badRaw
			}
			c.Spec.PodConfig = &weka.PodConfiguration{RoleTopologySpreadConstraints: rt}
		}
	}

	cases := []struct {
		family string
		roles  []string
	}{
		{"roleNodeSelector", sixRoles},
		{"roleAnnotations", sixRoles},
		{"podConfig.roleAffinity", fiveRoles},
		{"podConfig.roleTopologySpreadConstraints", fiveRoles},
	}
	for _, tc := range cases {
		for _, role := range tc.roles {
			t.Run(tc.family+"/"+role, func(t *testing.T) {
				c := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
				set(c, tc.family, role)
				errs := v.Validate(context.Background(), nil, c)
				if len(errs) != 1 {
					t.Fatalf("got %d errors (%v), want 1", len(errs), errs)
				}
				want := "spec." + tc.family + "." + role
				if !strings.HasPrefix(errs[0].Field, want) {
					t.Fatalf("wrong field path: %s (want prefix %s)", errs[0].Field, want)
				}
			})
		}
	}
}
