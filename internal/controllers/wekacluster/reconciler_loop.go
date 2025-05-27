package wekacluster

import (
	"context"
	"fmt"

	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/exec"
)

type ReadyForClusterizationContainers struct {
	Drive   []*weka.WekaContainer
	Compute []*weka.WekaContainer
	// containers that are not ready for clusterization (e.g. unhealthy)
	Ignored []*weka.WekaContainer
}

func (c *ReadyForClusterizationContainers) GetAll() []*weka.WekaContainer {
	var all []*weka.WekaContainer
	all = append(all, c.Drive...)
	all = append(all, c.Compute...)
	return all
}

func NewWekaClusterReconcileLoop(r *WekaClusterReconciler) *wekaClusterReconcilerLoop {
	mgr := r.Manager
	config := mgr.GetConfig()
	restClient := r.RestClient
	execService := exec.NewExecService(restClient, config)
	scheme := mgr.GetScheme()
	return &wekaClusterReconcilerLoop{
		Manager:         mgr,
		ExecService:     execService,
		Recorder:        mgr.GetEventRecorderFor("wekaCluster-controller"),
		SecretsService:  services.NewSecretsService(mgr.GetClient(), scheme, execService),
		RestClient:      restClient,
		GlobalThrottler: r.ThrottlingMap,
	}
}

type wekaClusterReconcilerLoop struct {
	Manager         ctrl.Manager
	ExecService     exec.ExecService
	Recorder        record.EventRecorder
	cluster         *weka.WekaCluster
	clusterService  services.WekaClusterService
	containers      []*weka.WekaContainer
	SecretsService  services.SecretsService
	RestClient      rest.Interface
	GlobalThrottler throttling.Throttler
	Throttler       throttling.Throttler
	// internal field used to store data in-memory between steps
	readyContainers *ReadyForClusterizationContainers
}

func (r *wekaClusterReconcilerLoop) FetchCluster(ctx context.Context, req ctrl.Request) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "FetchCluster")
	defer end()

	wekaCluster := &weka.WekaCluster{}
	err := r.getClient().Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		return err
	}

	r.cluster = wekaCluster
	r.clusterService = services.NewWekaClusterService(r.Manager, r.RestClient, wekaCluster)
	r.Throttler = r.GlobalThrottler.WithPartition("cluster/" + string(wekaCluster.GetUID()))

	return err
}

func (r *wekaClusterReconcilerLoop) RecordEvent(eventtype, reason, message string) error {
	if r.cluster == nil {
		return fmt.Errorf("cluster is not set")
	}
	if eventtype == "" {
		normal := v1.EventTypeNormal
		eventtype = normal
	}

	r.Recorder.Event(r.cluster, eventtype, reason, message)
	return nil
}
