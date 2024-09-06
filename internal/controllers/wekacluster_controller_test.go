package controllers

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	uzap "go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

func TestReconcile(t *testing.T) {
	ctx := context.Background()

	internalLogger := zapr.NewLogger(prettyconsole.NewLogger(uzap.DebugLevel))
	ctx, logger := instrumentation.GetLoggerForContext(ctx, &internalLogger, "TestReconcile")
	logger.Info("TestReconcile")

	manager := &stubManager{
		client: &stubClient{
			state: map[string]map[types.NamespacedName]client.Object{
				"*v1alpha1.WekaCluster": {
					types.NamespacedName{
						Name:      "test-cluster",
						Namespace: "test-namespace",
					}: &wekav1alpha1.WekaCluster{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-cluster",
							Namespace: "test-namespace",
						},
					},
				},
			},
		},
	}

	reconciler := NewWekaClusterController(manager)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
	}
	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("failed to reconcile: %v", err)
	}

	if !result.IsZero() {
		t.Errorf("unexpected result: %v", result)
	}

	if len(manager.client.state) != 0 {
		t.Errorf("unexpected client state: %v", manager.client.state)
	}
}

type stubManager struct {
	client *stubClient
	logger logr.Logger
}

// cluster.Cluster
func (m *stubManager) GetHTTPClient() *http.Client                          { return &http.Client{} }
func (m *stubManager) GetConfig() *rest.Config                              { return &rest.Config{} }
func (m *stubManager) GetCache() cache.Cache                                { return nil }
func (m *stubManager) GetScheme() *runtime.Scheme                           { return &runtime.Scheme{} }
func (m *stubManager) GetClient() client.Client                             { return m.client }
func (m *stubManager) GetFieldIndexer() client.FieldIndexer                 { return nil }
func (m *stubManager) GetEventRecorderFor(name string) record.EventRecorder { return nil }
func (m *stubManager) GetRESTMapper() meta.RESTMapper                       { return nil }
func (m *stubManager) GetAPIReader() client.Reader                          { return nil }

// Manager
func (m *stubManager) Add(runnable manager.Runnable) error                      { return nil }
func (m *stubManager) Elected() <-chan struct{}                                 { return nil }
func (m *stubManager) AddHealthzCheck(name string, check healthz.Checker) error { return nil }
func (m *stubManager) AddReadyzCheck(name string, check healthz.Checker) error  { return nil }
func (m *stubManager) Start(ctx context.Context) error                          { return nil }
func (m *stubManager) GetWebhookServer() webhook.Server                         { return nil }
func (m *stubManager) GetLogger() logr.Logger                                   { return m.logger }
func (m *stubManager) GetControllerOptions() config.Controller                  { return config.Controller{} }

// Client
type stubClient struct {
	// kind -> id -> object
	state map[string]map[types.NamespacedName]client.Object
}

// Reader
func (c *stubClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	_, logger, done := instrumentation.GetLogSpan(ctx, "WekaClusterReconcile", "namespace", key.Namespace, "cluster_name", key.Name)
	defer done()

	logger.Info("stubClient.Get")

	objectType := reflect.TypeOf(obj).String()
	logger.V(1).Info("objectType", "objectType", objectType)

	objects, ok := c.state[objectType]
	if !ok {
		err := apierrors.NewNotFound(schema.GroupResource{}, key.Name)
		logger.Error(err, "object type not found")
		return err
	}
	object, ok := objects[key]
	if !ok {
		err := apierrors.NewNotFound(schema.GroupResource{}, key.Name)
		logger.Error(err, "object not found")
		return err
	}

	objVal := reflect.ValueOf(obj)
	if objVal.Kind() != reflect.Ptr || objVal.IsNil() {
		err := apierrors.NewInternalError(apierrors.NewBadRequest("obj must be a non-nil pointer"))
		logger.Error(err, "obj must be a non-nil pointer")
		return err
	}

	storedVal := reflect.ValueOf(object)

	if storedVal.Elem().Type().AssignableTo(objVal.Elem().Type()) {
		objVal.Elem().Set(storedVal.Elem())
	} else {
		err := fmt.Errorf("object type mismatch: stored %v != requested %v", storedVal.Elem().Type(), objVal.Elem().Type())
		logger.Error(err, "object type mismatch")
		return err
	}

	return nil
}

func (c *stubClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return nil
}

// Writer
func (c *stubClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return nil
}

func (c *stubClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return nil
}

func (c *stubClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return nil
}

// Patch
func (c *stubClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return nil
}

func (c *stubClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return nil
}

// StatuClient
func (c *stubClient) Status() client.StatusWriter { return &stubStatusWriter{} }

// SubResourceClientConstructor
func (c *stubClient) SubResource(resource string) client.SubResourceClient { return nil }

// Client
func (c *stubClient) RESTMapper() meta.RESTMapper { return nil }
func (c *stubClient) Scheme() *runtime.Scheme     { return &runtime.Scheme{} }
func (c *stubClient) GroupVersionKindFor(obj runtime.Object) (gvk schema.GroupVersionKind, err error) {
	return schema.GroupVersionKind{}, nil
}
func (c *stubClient) IsObjectNamespaced(obj runtime.Object) (bool, error) { return false, nil }

// StatusWriter
type stubStatusWriter struct{}

func (w *stubStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return nil
}

func (w *stubStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return nil
}

func (w *stubStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return nil
}
