package controllers

import (
	"context"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const bootScriptConfigName = "weka-boot-scripts"

func NewContainerController(mgr ctrl.Manager, restClient rest.Interface) *ContainerController {
	config := mgr.GetConfig()
	kClient := mgr.GetClient()
	execService := exec.NewExecService(restClient, config)
	return &ContainerController{
		Client:        kClient,
		Scheme:        mgr.GetScheme(),
		Logger:        mgr.GetLogger().WithName("controllers").WithName("Container"),
		KubeService:   kubernetes.NewKubeService(mgr.GetClient()),
		ExecService:   execService,
		Manager:       mgr,
		RestClient:    restClient,
		ThrottlingMap: util.NewSyncMapThrottler(),
	}
}

type ContainerController struct {
	client.Client
	Scheme        *runtime.Scheme
	Logger        logr.Logger
	KubeService   kubernetes.KubeService
	ExecService   exec.ExecService
	Manager       ctrl.Manager
	RestClient    rest.Interface
	ThrottlingMap *util.ThrottlingSyncMap // TODO: Implement GC, so it will be cleaned up(maybe stored in different place as well) when containers are no more. Low priority as we dont expect lots of rotation
}

func (c *ContainerController) RunGC(ctx context.Context) {}

//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclients/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=driveclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=driveclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=driveclaims/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;update;create;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;update;create;watch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update;watch
//+kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;update;create;delete;watch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekamanualoperations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekamanualoperations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekamanualoperations/finalizers,verbs=update
//+kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get

// Reconcile reconciles a WekaContainer resource
func (r *ContainerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaContainerReconcile", "namespace", req.Namespace, "container_name", req.Name)
	defer end()

	ctx, cancel := context.WithTimeout(ctx, config.Config.Timeouts.ReconcileTimeout)
	defer cancel()

	container, err := r.refreshContainer(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Container not found, ignoring reconcile")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// NOTE: throttling map should be initialized before we define ReconciliationSteps,
	// otherwise the throttling will not work as expected
	if container.Status.Timestamps == nil {
		container.Status.Timestamps = make(map[string]metav1.Time)
	}

	logger.SetValues("mode", container.Spec.Mode, "uuid", string(container.GetUID()))
	steps := ContainerReconcileSteps(r, container)
	return steps.RunAsReconcilerResponse(ctx)
}

func (r *ContainerController) refreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "refreshContainer")
	defer end()

	container := &wekav1alpha1.WekaContainer{}
	if err := r.Get(ctx, req.NamespacedName, container); err != nil {
		return nil, errors.Wrap(err, "refreshContainer")
	}
	logger.SetStatus(codes.Ok, "Container refreshed")
	return container, nil
}

func (r *ContainerController) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		Owns(&v1.Pod{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaContainer}).
		Complete(wrappedReconcile)
}
