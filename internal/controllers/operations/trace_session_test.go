package operations

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type traceSessionTestManager struct {
	ctrl.Manager
	kubeClient client.Client
	scheme     *runtime.Scheme
}

func (m *traceSessionTestManager) GetClient() client.Client {
	return m.kubeClient
}

func (m *traceSessionTestManager) GetScheme() *runtime.Scheme {
	return m.scheme
}

func TestTraceSessionDeploymentPropagatesServiceAccount(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add WEKA types to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps types to scheme: %v", err)
	}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	mgr := &traceSessionTestManager{
		kubeClient: kubeClient,
		scheme:     scheme,
	}
	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "trace-policy",
			Namespace: "default",
			UID:       types.UID("trace-policy-uid"),
		},
	}
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-cluster",
			Namespace: "default",
			UID:       types.UID("weka-cluster-uid"),
		},
	}
	serviceAccountName := "weka-runtime"
	op := &MaintainTraceSession{
		cluster:  cluster,
		payload:  &weka.RemoteTracesSessionConfig{},
		mgr:      mgr,
		ownerRef: policy,
		containerDetails: weka.WekaOwnerDetails{
			Image:              "taskmon:latest",
			ImagePullSecret:    "weka-registry",
			ServiceAccountName: serviceAccountName,
		},
	}

	if err := op.EnsureDeployment(context.Background()); err != nil {
		t.Fatalf("EnsureDeployment() error = %v", err)
	}

	var deployment appsv1.Deployment
	key := client.ObjectKey{Name: "trace-session-" + policy.Name, Namespace: policy.Namespace}
	if err := kubeClient.Get(context.Background(), key, &deployment); err != nil {
		t.Fatalf("get trace session deployment: %v", err)
	}
	if got := deployment.Spec.Template.Spec.ServiceAccountName; got != serviceAccountName {
		t.Errorf("ServiceAccountName = %q, want %q", got, serviceAccountName)
	}
}
