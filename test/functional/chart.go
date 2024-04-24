package e2e

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"helm.sh/helm/v3/pkg/action"
)

type Chart struct {
	SystemTest
}

func (c *Chart) Run(t *testing.T) {
	t.Run("Ensure Namespace", c.EnsureNamespace)
	t.Run("Did Operator Start", c.DidInstallSucceed)
}

func debug(logger *instrumentation.SpanLogger) action.DebugLog {
	return func(format string, args ...interface{}) {
		if os.Getenv("DEBUG") == "true" {
			msg := fmt.Sprintf(format, args...)
			logger.Info(msg)
		}
	}
}

func (st *Chart) EnsureNamespace(t *testing.T) {
	_, _, done := instrumentation.GetLogSpan(st.Ctx, "EnsureNamespace")
	defer done()

	tests := []struct {
		namespace string
	}{
		{namespace: st.Namespace},
		{namespace: st.SystemNamespace},
	}

	for _, tt := range tests {
		ns := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: tt.namespace,
			},
		}
		t.Run(tt.namespace, func(t *testing.T) {
			if err := st.Get(st.Ctx, client.ObjectKeyFromObject(ns), ns); err != nil {
				if apierrors.IsNotFound(err) {
					t.Fatalf("namespace %s not found", tt.namespace)
				}
			}
		})
	}
}

func (st *Chart) DidInstallSucceed(t *testing.T) {
	_, _, done := instrumentation.GetLogSpan(st.Ctx, "DidOperatorStart")
	defer done()

	// Check if the CRDs are present
	t.Run("Check CRDs", st.CheckCRDs)
}

func (st *Chart) CheckCRDs(t *testing.T) {
	_, _, done := instrumentation.GetLogSpan(st.Ctx, "CheckCRDs")
	defer done()

	// No controller runtime API for CRDs
	// Need to use REST API instead

	cfg := st.Cfg
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create clientset: %v", err)
	}

	groups, _, err := clientset.Discovery().ServerGroupsAndResources()
	if err != nil {
		t.Fatalf("failed to get server resources: %v", err)
	}

	if len(groups) == 0 {
		t.Fatalf("expected server groups, got none")
	}

	idx := slices.IndexFunc(groups, func(g *metav1.APIGroup) bool {
		return g.Name == "weka.weka.io"
	})
	if idx == -1 {
		t.Error("expected weka group, got:")
		for _, g := range groups {
			t.Logf("group: %s", g.Name)
		}
	}

	wekaGroup := groups[idx]
	if len(wekaGroup.Versions) != 1 {
		t.Errorf("expected 1 version, got %d", len(wekaGroup.Versions))
	}
	if wekaGroup.Versions[0].GroupVersion != "weka.weka.io/v1alpha1" {
		t.Errorf("expected group version weka.weka.io/v1alpha1, got %s", wekaGroup.Versions[0].GroupVersion)
	}
	if wekaGroup.Versions[0].Version != "v1alpha1" {
		t.Errorf("expected version v1alpha1, got %s", wekaGroup.Versions[0].Version)
	}

	resourceList, err := clientset.Discovery().ServerResourcesForGroupVersion("weka.weka.io/v1alpha1")
	if err != nil {
		t.Fatalf("failed to get server resources for group version: %v", err)
	}
	if resourceList.GroupVersion != "weka.weka.io/v1alpha1" {
		t.Errorf("expected group version weka.weka.io/v1alpha1, got %s", resourceList.GroupVersion)
	}

	resources := resourceList.APIResources
	if len(resources) == 0 {
		t.Fatalf("expected API resources, got %d", len(resources))
	}

	expectedResources := []string{
		"driveclaims",
		"driveclaims/status",
		"wekaclients",
		"wekaclients/status",
		"wekaclusters",
		"wekaclusters/status",
		"wekacontainers",
		"wekacontainers/status",
	}
	for _, r := range expectedResources {
		idx := slices.IndexFunc(resources, func(apiResource metav1.APIResource) bool {
			return apiResource.Name == r
		})
		if idx == -1 {
			t.Errorf("expected resource %s not found", r)
		}
	}
}
