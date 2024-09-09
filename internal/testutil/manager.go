package testutil

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-logr/logr"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
)

type Manager interface {
	ctrl.Manager
	SetState(state map[string]map[types.NamespacedName]client.Object)
	GetObject(typeName string, key types.NamespacedName) client.Object
	UpdateObject(typeName string, key types.NamespacedName, obj client.Object)
}

func TestingManager() (*stubManager, error) {
	scheme := runtime.NewScheme()
	if err := wekav1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add wekav1alpha1 to scheme: %w", err)
	}
	manager := &stubManager{
		client: &stubClient{
			state: map[string]map[types.NamespacedName]client.Object{},
		},
		scheme: scheme,
	}
	return manager, nil
}

type stubManager struct {
	client *stubClient
	logger logr.Logger
	scheme *runtime.Scheme
}

func (m *stubManager) SetState(state map[string]map[types.NamespacedName]client.Object) {
	m.client.state = state
}

func (m *stubManager) GetObject(typeName string, key types.NamespacedName) client.Object {
	objects, ok := m.client.state[typeName]
	if !ok {
		return nil
	}
	object, ok := objects[key]
	if !ok {
		return nil
	}
	return object
}

func (m *stubManager) UpdateObject(typeName string, key types.NamespacedName, obj client.Object) {
	objects, ok := m.client.state[typeName]
	if !ok {
		objects = map[types.NamespacedName]client.Object{}
		m.client.state[typeName] = objects
	}
	objects[key] = obj
}

// cluster.Cluster
func (m *stubManager) GetHTTPClient() *http.Client                          { return &http.Client{} }
func (m *stubManager) GetConfig() *rest.Config                              { return &rest.Config{} }
func (m *stubManager) GetCache() cache.Cache                                { return nil }
func (m *stubManager) GetScheme() *runtime.Scheme                           { return m.scheme }
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
	_, logger, done := instrumentation.GetLogSpan(ctx, "WekaClusterReconcile")
	defer done()
	logger.V(1).Info("stubClient.List")

	objectType := reflect.TypeOf(list).String()
	if !strings.HasSuffix(objectType, "List") {
		err := fmt.Errorf("object must be a list")
		logger.Error(err, "object must be a list", "objectType", objectType)
		return err
	}
	singularType := objectType[:len(objectType)-4]
	objects, ok := c.state[singularType]
	if !ok {
		logger.V(1).Info("object type not found", "objectType", singularType)
		return nil
	}

	listVal := reflect.ValueOf(list)
	if listVal.Kind() != reflect.Ptr || listVal.IsNil() {
		err := apierrors.NewInternalError(apierrors.NewBadRequest("list must be a non-nil pointer"))
		logger.Error(err, "list must be a non-nil pointer")
		return err
	}

	itemsField := listVal.Elem().FieldByName("Items")
	if !itemsField.IsValid() {
		err := apierrors.NewInternalError(apierrors.NewBadRequest("list must have Items field"))
		logger.Error(err, "list must have Items field")
		return err
	}

	itemsField.Set(reflect.MakeSlice(itemsField.Type(), 0, 0))
	for _, object := range objects {
		itemsField.Set(reflect.Append(itemsField, reflect.ValueOf(object)))
	}

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
	_, logger, done := instrumentation.GetLogSpan(
		ctx,
		"WekaClusterReconcile",
		"namespace", obj.GetNamespace(),
		"cluster_name", obj.GetName(),
	)
	defer done()
	logger.V(1).Info("stubClient.Update")

	objectType := reflect.TypeOf(obj).String()
	logger.V(1).Info("objectType", "objectType", objectType)

	objects, ok := c.state[objectType]
	if !ok {
		c.state[objectType] = map[types.NamespacedName]client.Object{}
	}
	_, ok = objects[types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}]
	if !ok {
		err := apierrors.NewNotFound(schema.GroupResource{}, obj.GetName())
		logger.Error(err, "object not found")
		return err
	}
	objects[types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}] = obj

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
