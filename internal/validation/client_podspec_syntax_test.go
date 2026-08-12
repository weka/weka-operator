package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClientPodspecSyntax(t *testing.T) {
	v := clientPodspecSyntax{}
	base := func() *weka.WekaClient {
		return &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
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
	} else if !strings.Contains(errs[0].Detail, "rawTolerations") {
		t.Fatalf("missing rawTolerations hint: %s", errs[0].Detail)
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

	badNodeLabels := base()
	badNodeLabels.Spec.CsiConfig = &weka.ClientCsiConfig{
		Advanced: &weka.AdvancedCsiConfig{NodeLabels: map[string]string{"k": "bad value!"}},
	}
	if errs := v.Validate(context.Background(), nil, badNodeLabels); len(errs) != 1 {
		t.Fatalf("bad csiConfig.advanced.nodeLabels: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.csiConfig.advanced.nodeLabels") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badControllerTol := base()
	badControllerTol.Spec.CsiConfig = &weka.ClientCsiConfig{
		Advanced: &weka.AdvancedCsiConfig{ControllerTolerations: []corev1.Toleration{{Key: "k", Operator: "Bogus"}}},
	}
	if errs := v.Validate(context.Background(), nil, badControllerTol); len(errs) != 1 {
		t.Fatalf("bad csiConfig.advanced.controllerTolerations: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.csiConfig.advanced.controllerTolerations") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badControllerLabels := base()
	badControllerLabels.Spec.CsiConfig = &weka.ClientCsiConfig{
		Advanced: &weka.AdvancedCsiConfig{ControllerLabels: map[string]string{"bad key!": "x"}},
	}
	if errs := v.Validate(context.Background(), nil, badControllerLabels); len(errs) != 1 {
		t.Fatalf("bad csiConfig.advanced.controllerLabels: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.csiConfig.advanced.controllerLabels") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}

	badNodeTol := base()
	badNodeTol.Spec.CsiConfig = &weka.ClientCsiConfig{
		Advanced: &weka.AdvancedCsiConfig{NodeTolerations: []corev1.Toleration{{Key: "k", Operator: "Bogus"}}},
	}
	if errs := v.Validate(context.Background(), nil, badNodeTol); len(errs) != 1 {
		t.Fatalf("bad csiConfig.advanced.nodeTolerations: got %v", errs)
	} else if !strings.HasPrefix(errs[0].Field, "spec.csiConfig.advanced.nodeTolerations") {
		t.Fatalf("wrong field path: %s", errs[0].Field)
	}
}
