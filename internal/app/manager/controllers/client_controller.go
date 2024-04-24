/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/services"
)

// ClientReconciler reconciles a Client object
type ClientReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Manager  ctrl.Manager

	ConditionReady *condition.Ready

	Logger      logr.Logger
	ExecService services.ExecService

	// -- State dependent components
	// These may be nil depending on where we are int he reconciliation process
	CurrentInstance *wekav1alpha1.WekaClient
}

func NewClientReconciler(mgr ctrl.Manager) *ClientReconciler {
	return &ClientReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorderFor("weka-operator"),
		Logger:      mgr.GetLogger().WithName("controllers").WithName("WekaClient"),
		Manager:     mgr,
		ExecService: services.NewExecService(mgr),
	}
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile

func (r *ClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "Reconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	wekaClient, err := GetClient(ctx, req, r.Client)
	if err != nil {
		logger.Error(err, "Failed to get wekaClient")
		return ctrl.Result{}, err
	}
	if wekaClient == nil {
		logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	err = r.initState(ctx, wekaClient)
	if err != nil {
		logger.Error(err, "Failed to initialize state")
		return ctrl.Result{}, err
	}

	if wekaClient.GetDeletionTimestamp() != nil {
		err = r.handleDeletion(ctx, wekaClient)
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Deleting wekaClient")
		return ctrl.Result{}, nil
	}

	applicableNodes, err := getNodesByLabels(ctx, r.Client, wekaClient.Spec.NodeSelector)
	if err != nil {
		logger.Error(err, "Failed to get applicable nodes by labels")
		return ctrl.Result{}, err
	}

	// TODO: Right now single failure blocks other clients
	// Need to decouple it, and make similar parallel ensuring of each client and not one by one
	// Alternatively this could be moved to Container reconcile for parallesation
	// So container container will be responsible for translating and actually updating spec or status
	wg := sync.WaitGroup{}
	errs := make(chan error, len(applicableNodes))
	for _, node := range applicableNodes {
		wg.Add(1)
		go func(node string) {
			err := func(node string) error {
				return EnsureNodeDiscovered(ctx, r.Client, OwnerWekaObject{
					Image:           wekaClient.Spec.Image,
					ImagePullSecret: wekaClient.Spec.ImagePullSecret,
				}, node, r.ExecService)
			}(node)
			if err != nil {
				errs <- err
			}
			wg.Done()
		}(node)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		logger.Error(err, "Failed to ensure node discovered")
		return ctrl.Result{}, err
	}

	result, containers, err := r.ensureClientsWekaContainers(ctx, wekaClient, applicableNodes)
	if err != nil {
		logger.Error(err, "Failed to ensure weka containers")
		return result, err
	}
	if result.Requeue {
		return result, nil
	}

	_ = containers
	return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
}

func (r *ClientReconciler) RecordEvent(eventtype string, reason string, message string) error {
	if r.CurrentInstance == nil {
		return fmt.Errorf("current client is nil")
	}
	r.Recorder.Event(r.CurrentInstance, v1.EventTypeNormal, reason, message)
	return nil
}

// TODO: Factor the below  out into reconciler methods
// SetupWithManager sets up the controller with the Manager.
func (r *ClientReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconiler reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaClient{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(wrappedReconiler)
}

func (r *ClientReconciler) finalizeClient(ctx context.Context, client *wekav1alpha1.WekaClient) error {
	r.Logger.Info("Successfully finalized WekaClient")
	return nil
}

func (r *ClientReconciler) initState(ctx context.Context, wekaClient *wekav1alpha1.WekaClient) error {
	if wekaClient.GetFinalizers() == nil {
		wekaClient.SetFinalizers([]string{WekaFinalizer})
		err := r.Update(ctx, wekaClient)
		if err != nil {
			return errors.Wrap(err, "failed to update wekaClient")
		}
	}
	return nil
}

func (r *ClientReconciler) handleDeletion(ctx context.Context, wekaClient *wekav1alpha1.WekaClient) error {
	if controllerutil.ContainsFinalizer(wekaClient, WekaFinalizer) {
		if err := r.finalizeClient(ctx, wekaClient); err != nil {
			return errors.Wrap(err, "failed to finalize wekaClient")
		}
		controllerutil.RemoveFinalizer(wekaClient, WekaFinalizer)
		if err := r.Update(ctx, wekaClient); err != nil {
			return errors.Wrap(err, "failed to update wekaClient")
		}
	}
	return nil
}

func (r *ClientReconciler) ensureClientsWekaContainers(ctx context.Context, wekaClient *wekav1alpha1.WekaClient, nodes []string) (ctrl.Result, []*wekav1alpha1.WekaContainer, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureClientsWekaContainers", "namespace", wekaClient.Namespace, "name", wekaClient.Name)
	defer end()

	foundContainers := []*wekav1alpha1.WekaContainer{}
	size := len(nodes)
	if size == 0 {
		return ctrl.Result{Requeue: true}, nil, nil
	}

	for _, node := range nodes {
		wekaContainer, err := r.buildClientWekaContainer(ctx, wekaClient, node)
		if err != nil {
			return ctrl.Result{}, nil, err
		}
		err = ctrl.SetControllerReference(wekaClient, wekaContainer, r.Scheme)
		if err != nil {
			return ctrl.Result{Requeue: true}, nil, err
		}

		found := &wekav1alpha1.WekaContainer{}
		err = r.Get(ctx, client.ObjectKey{Namespace: wekaContainer.Namespace, Name: wekaContainer.Name}, found)
		if err != nil && apierrors.IsNotFound(err) {
			// TODO: Wasteful approach right now, each client fetches separately
			// We should have some small time-based cache here
			err := r.resolveJoinIps(ctx, wekaClient)
			if err != nil {
				return ctrl.Result{Requeue: true}, nil, err
			}
			// Always re-applying, either we had JoinIps set by user, or we have resolving re-populating them
			wekaContainer.Spec.JoinIps = wekaClient.Spec.JoinIps

			err = r.Create(ctx, wekaContainer)
			if err != nil {
				return ctrl.Result{Requeue: true}, nil, err
			}

			foundContainers = append(foundContainers, wekaContainer)
		} else {
			foundContainers = append(foundContainers, found)
		}
	}
	return ctrl.Result{}, foundContainers, nil
}

func (r *ClientReconciler) buildClientWekaContainer(ctx context.Context, wekaClient *wekav1alpha1.WekaClient, node string) (*wekav1alpha1.WekaContainer, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "buildClientWekaContainer", "node", node)
	defer end()

	cpuPolicy, err := ResolveCpuPolicy(ctx, r.Client, node, wekaClient.Spec.CpuPolicy)
	if err != nil {
		return nil, err
	}

	network, err := resources.GetContainerNetwork(wekaClient.Spec.NetworkSelector)
	if err != nil {
		return nil, err
	}

	var numCores int
	numCores = wekaClient.Spec.CoresNumber
	if wekaClient.Spec.CoresNumber == 0 {
		numCores = 1
	}

	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", wekaClient.ObjectMeta.Name, node),
			Namespace: wekaClient.Namespace,
			Labels:    map[string]string{"app": "weka-client", "clientName": wekaClient.ObjectMeta.Name},
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			NodeAffinity:        node,
			Port:                wekaClient.Spec.Port,
			AgentPort:           wekaClient.Spec.AgentPort,
			Image:               wekaClient.Spec.Image,
			ImagePullSecret:     wekaClient.Spec.ImagePullSecret,
			WekaContainerName:   fmt.Sprintf("%sclient", util.GetLastGuidPart(wekaClient.GetUID())),
			Mode:                "client",
			NumCores:            numCores,
			CpuPolicy:           cpuPolicy,
			CoreIds:             wekaClient.Spec.CoreIds,
			Network:             network,
			Hugepages:           1600,
			HugepagesSize:       "2Mi",
			WekaSecretRef:       v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: wekaClient.Spec.WekaSecretRef}},
			DriversDistService:  wekaClient.Spec.DriversDistService,
			JoinIps:             wekaClient.Spec.JoinIps,
			TracesConfiguration: wekaClient.Spec.TracesConfiguration,
		},
	}
	return container, nil
}

func (r *ClientReconciler) resolveJoinIps(ctx context.Context, wekaClient *wekav1alpha1.WekaClient) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "resolveJoinIps")
	defer end()

	emptyTarget := wekav1alpha1.ObjectReference{}
	if wekaClient.Spec.TargetCluster == emptyTarget {
		return nil
	}

	cluster, err := GetCluster(ctx, r.Client, wekaClient.Spec.TargetCluster)
	if err != nil {
		return err
	}

	joinIps, err := GetJoinIps(ctx, r.Client, cluster)
	if err != nil {
		return err
	}
	logger.Info("Resolved join ips", "joinIps", joinIps)

	wekaClient.Spec.JoinIps = joinIps
	// not commiting on purpose. If it will be - let it be. Just ad-hocy create for initial client create use. It wont be needed later
	// and new reconcilation loops will refresh it each time
	return nil
}

func GetClient(ctx context.Context, req ctrl.Request, r client.Reader) (*wekav1alpha1.WekaClient, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClient", "namespace", req.Namespace, "name", req.Name)
	defer end()

	wekaClient := &wekav1alpha1.WekaClient{}
	err := r.Get(ctx, req.NamespacedName, wekaClient)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaClient")
		return nil, err
	}
	return wekaClient, nil
}
