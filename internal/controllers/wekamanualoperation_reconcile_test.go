package controllers

import (
	"context"
	"reflect"
	"testing"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
)

// fakeManager is a minimal ctrl.Manager that only serves the client and scheme.
// The New*Operation constructors dereference the manager at construction time
// solely via GetClient()/GetScheme(); every other manager method is left nil and
// will panic if the reconcile path ever grows a dependency on it, which is the
// signal we want rather than a silently-wrong test.
type fakeManager struct {
	ctrl.Manager
	client client.Client
	scheme *runtime.Scheme
}

func (m *fakeManager) GetClient() client.Client   { return m.client }
func (m *fakeManager) GetScheme() *runtime.Scheme { return m.scheme }

func newReconcileScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add WEKA types to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 types to scheme: %v", err)
	}
	return scheme
}

// TestReconcilePropagatesServiceAccountToGeneratedContainers is the regression
// guard for PR #2685: it drives the real Reconcile switch end-to-end (fake
// client + fake manager) and asserts that spec.serviceAccountName set on the
// WekaManualOperation reaches the generated WekaContainer for every action that
// builds one. It drives the real constructor path, so it stays red against the
// pre-fix code where Reconcile hand-built a WekaOwnerDetails literal without
// ServiceAccountName. If a new case is ever added that bypasses ownerDetailsFrom,
// this fails.
func TestReconcilePropagatesServiceAccountToGeneratedContainers(t *testing.T) {
	const (
		ns              = "default"
		serviceAccount  = "weka-runtime"
		image           = "adhoc:latest" // non-empty and not weka-in-container, so resign accepts it
		imagePullSecret = "weka-registry"
	)
	tolerations := []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "weka", Effect: corev1.TaintEffectNoSchedule},
	}

	// Reconcile wraps the context with config.Config.Timeouts.ReconcileTimeout, which is
	// only populated by ConfigureEnv (not config.init()); under go test it is 0, so
	// WithTimeout would yield an already-expired context. Pin it for the duration of the test.
	prevTimeout := config.Config.Timeouts.ReconcileTimeout
	config.Config.Timeouts.ReconcileTimeout = time.Minute
	t.Cleanup(func() { config.Config.Timeouts.ReconcileTimeout = prevTimeout })

	cases := []struct {
		name    string
		action  weka.WekaManualOperationAction
		payload weka.ManualOperatorPayload
	}{
		{
			name:   "ForceResignDrives",
			action: weka.WekaManualOperationActionForceResignDrives,
			payload: weka.ManualOperatorPayload{
				ForceResignDrives: &weka.ForceResignDrivesPayload{NodeName: "worker-node-1"},
			},
		},
		{
			name:   "DiscoverDrives",
			action: weka.WekaManualOperationActionDiscoverDrives,
			payload: weka.ManualOperatorPayload{
				DiscoverDrives: &weka.DiscoverDrivesPayload{NodeSelector: map[string]string{}},
			},
		},
		{
			name:   "EnsureNICs",
			action: weka.WekaManualOperationActionEnsureNICs,
			payload: weka.ManualOperatorPayload{
				EnsureNICs: &weka.EnsureNICsPayload{NodeSelector: map[string]string{}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := newReconcileScheme(t)

			imageCopy := image
			imagePullSecretCopy := imagePullSecret
			op := &weka.WekaManualOperation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-manualop",
					Namespace: ns,
					UID:       types.UID("manualop-uid-" + tc.name),
				},
				Spec: weka.WekaManualOperationSpec{
					Action:             tc.action,
					Image:              &imageCopy,
					ImagePullSecret:    &imagePullSecretCopy,
					ServiceAccountName: serviceAccount,
					Tolerations:        tolerations,
					Payload:            tc.payload,
				},
			}

			// GetNamespace (resign) reads the Namespace object; the drive/NIC
			// discovery steps list Nodes via the node selector. Provide both so
			// every action reaches its EnsureContainer(s) step.
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "worker-node-1", UID: types.UID("node-uid-1")},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(op, namespace, node).
				WithStatusSubresource(&weka.WekaManualOperation{}).
				// GetOwnedContainers filters by the metadata.ownerReferences.uid field
				// index; the real manager registers it in cmd/manager/main.go, and the
				// fake client rejects the field selector unless we mirror it here.
				WithIndex(&weka.WekaContainer{}, "metadata.ownerReferences.uid", func(rawObj client.Object) []string {
					wekaContainer, ok := rawObj.(*weka.WekaContainer)
					if !ok {
						return nil
					}
					owner := metav1.GetControllerOf(wekaContainer)
					if owner == nil {
						return nil
					}
					return []string{string(owner.UID)}
				}).
				Build()

			r := &WekaManualOperationReconciler{
				Client: fakeClient,
				Scheme: scheme,
				Mgr:    &fakeManager{client: fakeClient, scheme: scheme},
			}

			// Each action creates its container, then requeues on the poll step
			// (freshly-created container has no execution result yet), so Reconcile
			// returns a clean requeue and the created container is observable.
			if _, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: op.Name, Namespace: op.Namespace},
			}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			var list weka.WekaContainerList
			if err := fakeClient.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
				t.Fatalf("list WekaContainers: %v", err)
			}
			if len(list.Items) != 1 {
				t.Fatalf("expected 1 generated WekaContainer, got %d", len(list.Items))
			}
			// Assert every field these actions consume from ownerDetailsFrom, not
			// just the one that regressed. Labels is centralized too but unreachable
			// via WekaContainerSpec here — these actions set container labels from
			// ownerRef.GetLabels() directly, so it isn't asserted.
			got := list.Items[0].Spec
			if got.ServiceAccountName != serviceAccount {
				t.Errorf("generated WekaContainer ServiceAccountName = %q, want %q", got.ServiceAccountName, serviceAccount)
			}
			if got.Image != image {
				t.Errorf("generated WekaContainer Image = %q, want %q", got.Image, image)
			}
			if got.ImagePullSecret != imagePullSecret {
				t.Errorf("generated WekaContainer ImagePullSecret = %q, want %q", got.ImagePullSecret, imagePullSecret)
			}
			if !reflect.DeepEqual(got.Tolerations, tolerations) {
				t.Errorf("generated WekaContainer Tolerations = %#v, want %#v", got.Tolerations, tolerations)
			}
		})
	}
}
