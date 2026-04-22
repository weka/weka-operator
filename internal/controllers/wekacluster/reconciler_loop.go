package wekacluster

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
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

// GetAllSteps combines all reconciliation steps into a single ordered list
func (loop *wekaClusterReconcilerLoop) GetAllSteps() []lifecycle.Step {
	var steps []lifecycle.Step

	// Initial container state - should always be first
	steps = append(steps, &lifecycle.SimpleStep{
		Run: loop.getCurrentContainers,
	})

	// Throttled metrics steps - can run in parallel and are isolated from main flow
	steps = append(steps, GetThrottledMetricsSteps(loop)...)

	// Manual pause path - when paused=true and cluster is not actively being deleted
	// (either not marked for deletion, or deletion was cancelled via cancelDeletion)
	// Deletion/creation paths are mutually exclusive
	steps = append(steps,
		&lifecycle.GroupedSteps{
			Name: "DeletionPath",
			Predicates: lifecycle.Predicates{
				loop.cluster.IsMarkedForDeletion,
				lifecycle.IsNotFunc(loop.ClusterDeletionCancelled),
			},
			Steps:           GetDeletionSteps(loop),
			FinishOnSuccess: true,
		},
		// Recovery of paused containers when deletion is cancelled and cluster is NOT manually paused
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.ClusterDeletionCancelled,
				lifecycle.IsNotFunc(loop.ClusterIsPaused),
			},
			Run:             loop.RecoverPausedContainers,
			ContinueOnError: true,
		},
		// Recovery of paused containers when paused is explicitly set to false
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.ClusterIsExplicitlyUnpaused,
				lifecycle.IsNotFunc(loop.cluster.IsMarkedForDeletion),
				loop.HasPausedContainers,
			},
			Run:             loop.RecoverPausedContainers,
			ContinueOnError: true,
		},
		// Reset cluster status to Ready once suspended state is resolved
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.ClusterStatusIsSuspended,
				lifecycle.IsNotFunc(loop.ClusterIsPaused),
				lifecycle.IsNotFunc(loop.HasPausedContainers),
			},
			Run: loop.SetReadyStatus,
		},
	)

	steps = append(steps, GetClusterSetupSteps(loop)...)
	steps = append(steps, GetClusterCreationSteps(loop)...)
	steps = append(steps, GetCredentialSteps(loop)...)
	steps = append(steps, GetPostClusterSteps(loop)...)

	return steps
}

func (r *wekaClusterReconcilerLoop) FetchCluster(ctx context.Context, req ctrl.Request) error {
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "FetchCluster")
	defer spanLogger.End()

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

func (r *wekaClusterReconcilerLoop) RecordEventThrottled(eventtype, reason, message string, interval time.Duration) error {
	if !r.Throttler.ShouldRun(eventtype+reason, &throttling.ThrottlingSettings{
		Interval:                    interval,
		DisableRandomPreSetInterval: true,
	}) {
		return nil
	}

	return r.RecordEvent(eventtype, reason, message)
}

func (r *wekaClusterReconcilerLoop) ClusterDeletionCancelled() bool {
	return r.cluster.Spec.GetOverrides().CancelDeletion
}

func (r *wekaClusterReconcilerLoop) RecoverPausedContainers(ctx context.Context) error {
	return r.ensureContainersNotPaused(ctx, "")
}

func (r *wekaClusterReconcilerLoop) SetReadyStatus(ctx context.Context) error {
	return r.updateClusterStatusIfNotEquals(ctx, weka.WekaClusterStatusReady)
}
