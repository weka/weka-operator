package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-k8s-api/util"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
	"go.opentelemetry.io/otel/codes"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func NewWekaClusterReconcileLoop(mgr ctrl.Manager, restClient rest.Interface) *wekaClusterReconcilerLoop {
	config := mgr.GetConfig()
	execService := exec.NewExecService(restClient, config)
	scheme := mgr.GetScheme()
	return &wekaClusterReconcilerLoop{
		Manager:        mgr,
		ExecService:    execService,
		SecretsService: services.NewSecretsService(mgr.GetClient(), scheme, execService),
		RestClient:     restClient,
	}
}

type wekaClusterReconcilerLoop struct {
	Manager        ctrl.Manager
	ExecService    exec.ExecService
	cluster        *wekav1alpha1.WekaCluster
	clusterService services.WekaClusterService
	containers     []*wekav1alpha1.WekaContainer
	SecretsService services.SecretsService
	RestClient     rest.Interface
}

func (r *wekaClusterReconcilerLoop) FetchCluster(ctx context.Context, req ctrl.Request) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "FetchCluster")
	defer end()

	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.getClient().Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		return err
	}

	r.cluster = wekaCluster
	r.clusterService = services.NewWekaClusterService(r.Manager, r.RestClient, wekaCluster)

	return err
}

func (r *wekaClusterReconcilerLoop) EnsureWekaContainers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		keys := make([]string, 0, len(allocator.WekaClusterTemplates))
		for k := range allocator.WekaClusterTemplates {
			keys = append(keys, k)
		}
		err := errors.New("template not found")
		logger.Error(err, "", "template", cluster.Spec.Template, "keys", keys)
		return err
	}

	currentContainers := r.containers
	missingContainers, err := BuildMissingContainers(ctx, cluster, template, currentContainers)
	if err != nil {
		logger.Error(err, "Failed to create missing containers")
		return err
	}
	for _, container := range missingContainers {
		if err := ctrl.SetControllerReference(cluster, container, r.Manager.GetScheme()); err != nil {
			logger.Error(err, "Failed to set controller reference")
			return err
		}
	}

	if len(missingContainers) == 0 {
		return nil
	}

	resourcesAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		logger.Error(err, "Failed to create resources allocator")
		return err
	}

	k8sClient := r.Manager.GetClient()
	if len(currentContainers) == 0 {
		logger.InfoWithStatus(codes.Unset, "Ensuring cluster-level allocation")
		//TODO: should've be just own step function
		err = resourcesAllocator.AllocateClusterRange(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to allocate cluster range")
			return err
		}
		err := k8sClient.Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return err
		}
		// update weka cluster status
	}

	var joinIps []string
	if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) || cluster.IsExpand() {
		//TODO: Update-By-Expansion, cluster-side join-ips until there are own containers
		allowExpansion := false
		err := services.ClustersJoinIps.RefreshJoinIps(ctx, currentContainers, cluster)
		if err != nil {
			allowExpansion = true
		}
		joinIps, err = services.ClustersJoinIps.GetJoinIps(ctx, cluster.Name, cluster.Namespace)
		// at this point we should have join ips, if not, we should allow expansion
		if len(joinIps) == 0 {
			allowExpansion = true
		}
		if err != nil && len(cluster.Spec.ExpandEndpoints) != 0 && allowExpansion { //TO
			joinIps = cluster.Spec.ExpandEndpoints
		} else {
			if err != nil {
				logger.Error(err, "Failed to get join ips")
				return err
			}
		}
	}

	errs := []error{}

	allContainers := []*wekav1alpha1.WekaContainer{}

	for _, container := range missingContainers {
		if len(joinIps) != 0 {
			container.Spec.JoinIps = joinIps
		}
		err = r.getClient().Create(ctx, container)
		if err != nil {
			logger.Error(err, "Failed to create container", "name", container.Name)
			errs = append(errs, err)
			continue
		}
		allContainers = append(allContainers, container)
	}
	allContainers = append(currentContainers, allContainers...)
	r.containers = allContainers

	if len(errs) != 0 {
		err := fmt.Errorf("failed to create containers: %v", errs)
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) getCurrentContainers(ctx context.Context) error {
	currentContainers := discovery.GetClusterContainers(ctx, r.getClient(), r.cluster, "")
	r.containers = currentContainers
	return nil
}

func (r *wekaClusterReconcilerLoop) getClient() client.Client {
	return r.Manager.GetClient()
}

func (r *wekaClusterReconcilerLoop) ClusterIsInGracefulDeletion() bool {
	if !r.cluster.IsMarkedForDeletion() {
		return false
	}

	deletionTime := r.cluster.GetDeletionTimestamp().Time
	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	hitTimeout := deletionTime.Add(gracefulDestroyDuration)
	return hitTimeout.After(time.Now())
}

func (r *wekaClusterReconcilerLoop) InitState(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	wekaCluster := r.cluster
	if !controllerutil.ContainsFinalizer(wekaCluster, WekaFinalizer) {

		wekaCluster.Status.InitStatus()
		wekaCluster.Status.LastAppliedImage = wekaCluster.Spec.Image

		err := r.getClient().Status().Update(ctx, wekaCluster)
		if err != nil {
			logger.Error(err, "failed to init states")
		}

		if updated := controllerutil.AddFinalizer(wekaCluster, WekaFinalizer); updated {
			logger.Info("Adding Finalizer for weka cluster")
			if err := r.getClient().Update(ctx, wekaCluster); err != nil {
				logger.Error(err, "Failed to update custom resource to add finalizer")
				return err
			}

			if err := r.getClient().Get(ctx, client.ObjectKey{Namespace: wekaCluster.Namespace, Name: wekaCluster.Name}, r.cluster); err != nil {
				logger.Error(err, "Failed to re-fetch data")
				return err
			}
			logger.Info("Finalizer added for wekaCluster", "conditions", len(wekaCluster.Status.Conditions))
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) HandleGracefulDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	if cluster.Status.Status != wekav1alpha1.WekaClusterStatusGracePeriod {
		cluster.Status.Status = wekav1alpha1.WekaClusterStatusGracePeriod
		err := r.getClient().Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return err
		}
	}

	err := r.ensureContainersPaused(ctx, wekav1alpha1.WekaContainerModeS3)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, wekav1alpha1.WekaContainerModeNfs)
	if err != nil {
		return err
	}

	err = r.ensureContainersPaused(ctx, "")
	if err != nil {
		return err
	}

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)

	logger.Info("Cluster is in graceful deletion", "deletionTime", deletionTime)
	return nil
}

func (r *wekaClusterReconcilerLoop) ensureContainersPaused(ctx context.Context, mode string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureContainersPaused", "mode", mode)
	defer end()

	containers := r.containers

	pausedStatus := strings.ToUpper(string(wekav1alpha1.ContainerStatePaused))
	var notPausedContainers []string

	for _, container := range containers {
		if mode != "" && container.Spec.Mode != mode {
			continue
		}
		if container.Spec.State != wekav1alpha1.ContainerStatePaused {
			container.Spec.State = wekav1alpha1.ContainerStatePaused
			err := r.getClient().Update(ctx, container)
			if err != nil {
				logger.Error(err, "Failed to update container state")
				return err
			}
		}

		if container.Status.Status != pausedStatus {
			notPausedContainers = append(notPausedContainers, container.Name)
		}
	}

	if len(notPausedContainers) != 0 {
		err := fmt.Errorf("not all %s containers are in %s status, missing: %v", mode, pausedStatus, notPausedContainers)
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := r.cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)
	logger.Debug("Not graceful deletion", "deletionTime", deletionTime, "now", time.Now(), "gracefulDestroyDuration", gracefulDestroyDuration)

	cluster := r.cluster
	if cluster.Status.Status != wekav1alpha1.WekaClusterStatusDestroying {
		cluster.Status.Status = wekav1alpha1.WekaClusterStatusDestroying
		err := r.getClient().Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return err
		}
	}

	if controllerutil.ContainsFinalizer(r.cluster, WekaFinalizer) {
		logger.Info("Performing Finalizer Operations for wekaCluster before delete CR")

		// Perform all operations required before remove the finalizer and allow
		// the Kubernetes API to remove the custom resource.
		err := r.finalizeWekaCluster(ctx)
		if err != nil {
			return err
		}

		logger.Info("Removing Finalizer for wekaCluster after successfully perform the operations")
		if ok := controllerutil.RemoveFinalizer(r.cluster, WekaFinalizer); !ok {
			err := errors.New("Failed to remove finalizer for wekaCluster")
			return err
		}

		if err := r.getClient().Update(ctx, r.cluster); err != nil {
			logger.Error(err, "Failed to remove finalizer for wekaCluster")
			return err
		}

	}
	return nil
}

func (r *wekaClusterReconcilerLoop) finalizeWekaCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService

	err := clusterService.EnsureNoContainers(ctx, wekav1alpha1.WekaContainerModeS3)
	if err != nil {
		return err
	}

	err = clusterService.EnsureNoContainers(ctx, wekav1alpha1.WekaContainerModeNfs)
	if err != nil {
		return err
	}

	err = clusterService.EnsureNoContainers(ctx, "")
	if err != nil {
		return err
	}

	if cluster.Status.Status != wekav1alpha1.WekaClusterStatusDeallocating {
		cluster.Status.Status = wekav1alpha1.WekaClusterStatusDeallocating
		err := r.getClient().Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return err
		}
	}

	resourcesAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		return err
	}

	err = resourcesAllocator.DeallocateCluster(ctx, cluster)
	if err != nil {
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureLoginCredentials(ctx context.Context) error {
	return r.SecretsService.EnsureLoginCredentials(ctx, r.cluster)
}

func (r *wekaClusterReconcilerLoop) HandleSpecUpdates(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "HandleSpecUpdates")
	defer end()

	cluster := r.cluster
	containers := r.containers

	updatableSpec := NewUpdatableClusterSpec(&cluster.Spec)
	// Preserving whole Spec for more generic approach on status, while being able to update only specific fields on containers
	specHash, err := util2.HashStruct(updatableSpec)
	if err != nil {
		return err
	}
	if specHash != cluster.Status.LastAppliedSpec {
		logger.Info("Cluster spec has changed, updating containers")
		for _, container := range containers {
			changed := false
			additionalMemory := updatableSpec.AdditionalMemory.GetForMode(container.Spec.Mode)
			if container.Spec.AdditionalMemory != additionalMemory {
				container.Spec.AdditionalMemory = additionalMemory
				changed = true
			}

			tolerations := util.ExpandTolerations([]v1.Toleration{}, updatableSpec.Tolerations, updatableSpec.RawTolerations)
			if !reflect.DeepEqual(container.Spec.Tolerations, tolerations) {
				container.Spec.Tolerations = tolerations
				changed = true
			}

			if container.Spec.DriversDistService != updatableSpec.DriversDistService {
				container.Spec.DriversDistService = updatableSpec.DriversDistService
				changed = true
			}

			if container.Spec.ImagePullSecret != updatableSpec.ImagePullSecret {
				container.Spec.ImagePullSecret = updatableSpec.ImagePullSecret
				changed = true
			}

			if changed {
				if err := r.getClient().Update(ctx, container); err != nil {
					return err
				}
			}
		}

		logger.Info("Updating last applied spec", "lastAppliedSpec", cluster.Status.LastAppliedSpec, "newSpec", specHash)
		cluster.Status.LastAppliedSpec = specHash
		if err := r.getClient().Status().Update(ctx, cluster); err != nil {
			return err
		}

	}
	return nil
}

func (r *wekaClusterReconcilerLoop) AllContainersReady(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers := r.containers

	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
			continue // ignoring envoy as not part of cluster and we can figure out at later stage
		}
		if container.GetDeletionTimestamp() != nil {
			logger.Debug("Container is being deleted, rejecting cluster create", "container_name", container.Name)
			return lifecycle.NewWaitError(errors.New("Container " + container.Name + " is being deleted, rejecting cluster create"))
		}

		if container.Status.Status != "Running" {
			logger.Debug("Container is not running yet", "container_name", container.Name)
			return lifecycle.NewWaitError(errors.New("containers not ready"))
		}
	}
	logger.InfoWithStatus(codes.Ok, "Containers are ready")
	return nil

}

func (r *wekaClusterReconcilerLoop) FormCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	wekaCluster := r.cluster
	wekaClusterService := r.clusterService
	containers := r.containers

	if wekaCluster.Spec.ExpandEndpoints == nil {
		err := wekaClusterService.FormCluster(ctx, containers)
		if err != nil {
			logger.Error(err, "Failed to form cluster")
			return lifecycle.NewWaitError(err)
		}
		return nil
		// TODO: We might want to capture specific errors, and return "unknown"/bigger errors
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) WaitForContainersJoin(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers := r.containers

	// Ensure all containers are up in the cluster
	logger.Debug("Ensuring all containers are up in the cluster")
	joinedContainers := 0
	for _, container := range containers {
		if !container.ShouldJoinCluster() {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)
			return lifecycle.NewWaitError(errors.New("container did not join cluster yet"))
		} else {
			if r.cluster.Status.ClusterID == "" {
				r.cluster.Status.ClusterID = container.Status.ClusterID
				err := r.getClient().Status().Update(ctx, r.cluster)
				if err != nil {
					return err
				}
				logger.Info("Container joined cluster successfully", "container_name", container.Name)
			}
			joinedContainers++
		}
		if container.Status.ClusterContainerID == nil {
			err := fmt.Errorf("container %s does not have a cluster container id", container.Name)
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) WaitForDrivesAdd(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers := r.containers
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
			if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) {
				logger.Info("Containers did not add drives yet", "container", container.Name)
				logger.InfoWithStatus(codes.Unset, "Containers did not add drives yet")
				return lifecycle.NewWaitError(errors.New("containers did not add drives yet"))
			}
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) StartIo(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers := r.containers

	if len(containers) == 0 {
		err := errors.New("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	kubeExecTimeout := config.Config.Timeouts.KubeExecTimeout
	if kubeExecTimeout < 10*time.Minute {
		kubeExecTimeout = 10 * time.Minute
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, execInContainer, &kubeExecTimeout)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	cmd := "wekaauthcli cluster start-io"
	_, stderr, err := executor.ExecNamed(ctx, "StartIO", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Failed to start-io")
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}
	logger.InfoWithStatus(codes.Ok, "IO started", "time_taken", time.Since(r.cluster.CreationTimestamp.Time).String())
	return nil
}

func (r *wekaClusterReconcilerLoop) ApplyCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	wekaService := services.NewWekaService(r.ExecService, container)

	ensureUser := func(secretName string) error {
		// fetch secret from k8s
		username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, secretName)
		if username == "admin" {
			// force switch back to operator user
			username = cluster.GetOperatorClusterUsername()
		}
		if err != nil {
			return err
		}
		err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
		if err != nil {
			return err
		}
		return nil
	}

	logger.WithValues("user_name", cluster.GetOperatorClusterUsername()).Info("Ensuring operator user")
	if err := ensureUser(cluster.GetOperatorSecretName()); err != nil {
		logger.Error(err, "Failed to apply operator user credentials")
		return err
	}

	logger.WithValues("user_name", cluster.GetUserClusterUsername()).Info("Ensuring admin user")
	if err := ensureUser(cluster.GetUserSecretName()); err != nil {
		logger.Error(err, "Failed to apply admin user credentials")
		return err
	}

	// change k8s secret to use proper account
	err := r.SecretsService.UpdateOperatorLoginSecret(ctx, cluster)
	if err != nil {
		return err
	}

	// Cannot delete admin at is used until we apply credential here, i.e creating new users
	// TODO: Delete later? How can we know that all pod updated their secrets? Or just, delay by 5 minutes? Docs says it's 1 minute

	//err := wekaService.EnsureNoUser(ctx, "admin")
	//if err != nil {
	//	return err
	//}
	logger.Info("Cluster credentials applied")
	return nil

}

func (r *wekaClusterReconcilerLoop) getUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error) {
	secret := &v1.Secret{}
	err := r.getClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		return "", "", err
	}
	username := secret.Data["username"]
	password := secret.Data["password"]
	return string(username), string(password), nil
}

func (r *wekaClusterReconcilerLoop) ensureClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	containers := r.containers

	activeContainer, err := discovery.SelectActiveContainerWithRole(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	err = r.SecretsService.EnsureClientLoginCredentials(ctx, cluster, activeContainer)
	if err != nil {
		logger.Error(err, "Failed to apply client login credentials")
		return err
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) DeleteAdminUser(ctx context.Context) error {
	container := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, container)
	return wekaService.EnsureNoUser(ctx, "admin")
}

func (r *wekaClusterReconcilerLoop) EnsureDefaultFS(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDefaultFs")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	err = wekaService.CreateFilesystemGroup(ctx, "default")
	if err != nil {
		var fsGroupExists *services.FilesystemGroupExists
		if !errors.As(err, &fsGroupExists) {
			logger.Error(err, "DID NOT detect FS as already exists")
			return err
		}
	}

	// This defaults are not meant to be configurable, as instead weka should not require them.
	// Until then, user configuratino post cluster create

	var thinProvisionedLimitsConfigFS int64 = 100 * 1024 * 1024 * 1024 // half a total capacity allocated for thin provisioning
	thinProvisionedLimitsDefault := status.Capacity.TotalBytes / 10    // half a total capacity allocated for thin provisioning
	fsReservedCapacity := status.Capacity.TotalBytes / 100
	var configFsSize int64 = 10 * 1024 * 1024 * 1024
	var defaultFsSize int64 = 1 * 1024 * 1024 * 1024

	if defaultFsSize > thinProvisionedLimitsDefault {
		thinProvisionedLimitsDefault = defaultFsSize
	}

	err = wekaService.CreateFilesystem(ctx, ".config_fs", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsConfigFS, 10),
		ThickProvisioningCapacity: strconv.FormatInt(configFsSize, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		var fsExists *services.FilesystemExists
		if !errors.As(err, &fsExists) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsDefault, 10),
		ThickProvisioningCapacity: strconv.FormatInt(fsReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		var fsExists *services.FilesystemExists
		if !errors.As(err, &fsExists) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "default filesystem ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) SelectS3Containers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var s3Containers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			s3Containers = append(s3Containers, container)
		}
	}
	return s3Containers
}

func (r *wekaClusterReconcilerLoop) SelectNfsContainers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var nfsContainers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeNfs {
			nfsContainers = append(nfsContainers, container)
		}
	}
	return nfsContainers
}

func (r *wekaClusterReconcilerLoop) EnsureS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureS3Cluster")
	defer end()

	cluster := r.cluster

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	// check if s3 cluster already exists
	s3Cluster, err := wekaService.GetS3Cluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to get S3 cluster")
		return err
	}

	if s3Cluster.Active || len(s3Cluster.S3Hosts) > 0 {
		logger.Info("S3 cluster already exists")
		return nil
	}

	s3Containers := r.SelectS3Containers(r.containers)
	containerIds := []int{}
	for _, c := range s3Containers {
		if c.Status.ClusterContainerID == nil {
			err := fmt.Errorf("container %s does not have a cluster container id", c.Name)
			return err
		}
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	err = wekaService.CreateS3Cluster(ctx, services.S3Params{
		EnvoyPort:      cluster.Status.Ports.LbPort,
		EnvoyAdminPort: cluster.Status.Ports.LbAdminPort,
		S3Port:         cluster.Status.Ports.S3Port,
		ContainerIds:   containerIds,
	})
	if err != nil {
		if !errors.As(err, &services.S3ClusterExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "S3 cluster ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) ShouldDestroyS3Cluster() bool {
	if !r.cluster.Spec.AllowS3ClusterDestroy {
		return false
	}

	// if spec contains desired S3 containers, do not destroy the cluster
	template, ok := allocator.GetTemplateByName(r.cluster.Spec.Template, *r.cluster)
	if !ok {
		return false
	}
	if template.S3Containers > 0 {
		return false
	}

	containers := r.SelectS3Containers(r.containers)

	// if there are more that 1 S3 container, we should not destroy the cluster
	if len(containers) > 1 {
		return false
	}

	// if S3 cluster was not created, we should not destroy it
	if !meta.IsStatusConditionTrue(r.cluster.Status.Conditions, condition.CondS3ClusterCreated) {
		return false
	}

	return true
}

func (r *wekaClusterReconcilerLoop) DestroyS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)

	s3ContainerIds, err := wekaService.ListS3ClusterContainers(ctx)
	if err != nil {
		return err
	}
	logger.Info("S3 cluster containers", "containers", s3ContainerIds)

	if len(s3ContainerIds) > 1 {
		err := fmt.Errorf("more than one container in S3 cluster: %v", s3ContainerIds)
		return lifecycle.NewWaitError(err)
	}

	logger.Info("Destroying S3 cluster")
	err = wekaService.DeleteS3Cluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to delete S3 cluster")
		return err
	}

	// invalidate S3 cluster created condition
	changed := meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondS3ClusterCreated,
		Status: metav1.ConditionFalse,
		Reason: "DestroyS3Cluster",
	})
	if changed {
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureNfs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNfs")
	defer end()

	containers := r.SelectNfsContainers(r.containers)

	execInContainer := discovery.SelectActiveContainer(containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)
	containerIds := []int{}
	for _, c := range containers {
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	err := wekaService.ConfigureNfs(ctx, services.NFSParams{
		ConfigFilesystem: ".config_fs",
	})

	if err != nil {
		if !errors.As(err, &services.NfsInterfaceGroupExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "NFS ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) applyClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClientLoginCredentials")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetClientSecretName())
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "regular")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "Client login credentials applied")
	return nil
}

func (r *wekaClusterReconcilerLoop) applyCSILoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyCSILoginCredentials")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetCSISecretName())
	if err != nil {
		err = fmt.Errorf("failed to get client login credentials: %w", err)
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "CSI login credentials applied")
	return nil
}

func (r *wekaClusterReconcilerLoop) configureWekaHome(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "configureWekaHome")
	defer end()

	wekaCluster := r.cluster
	containers := r.containers

	config, err := domain.GetWekahomeConfig(wekaCluster)
	if err != nil {
		return err
	}

	if config.Endpoint == "" {
		return nil // explicitly asked not to configure
	}

	driveContainer, err := discovery.SelectActiveContainerWithRole(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, driveContainer)
	err = wekaService.SetWekaHome(ctx, config)
	if err != nil {
		return err
	}
	return err
}

func (r *wekaClusterReconcilerLoop) handleUpgrade(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService

	if cluster.Spec.Image != cluster.Status.LastAppliedImage {
		logger.Info("Image upgrade sequence")
		driveContainers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeDrive)
		if err != nil {
			return err
		}
		// before upgrade, if if all drive nodes are still in old version - invoke upgrade prepare commands
		prepareForUpgrade := true
		for _, container := range driveContainers {
			//i.e if any container already on new target version - we should not prepare for drive phase
			if container.Spec.Image == cluster.Spec.Image {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeDrives(ctx, driveContainers, cluster.Spec.Image)
			if err != nil {
				return err
			}
		}

		wekaService := services.NewWekaService(r.ExecService, discovery.SelectActiveContainer(r.containers))
		status, err := wekaService.GetWekaStatus(ctx)
		if err != nil {
			return err
		}
		if status.Status != "OK" {
			return lifecycle.NewWaitError(errors.New("Weka status is not OK, waiting to stabilize. status:" + status.Status))
		}

		uController := NewUpgradeController(r.getClient(), driveContainers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		computeContainers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeCompute)
		if err != nil {
			return err
		}

		prepareForUpgrade = true
		// if any compute container changed version - do not prepare for compute
		for _, container := range computeContainers {
			if container.Spec.Image == cluster.Spec.Image {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeCompute(ctx, computeContainers, cluster.Spec.Image)
			if err != nil {
				return err
			}
		}

		uController = NewUpgradeController(r.getClient(), computeContainers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		s3Containers, err := clusterService.GetOwnedContainers(ctx, wekav1alpha1.WekaContainerModeS3)
		if err != nil {
			return err
		}
		prepareForUpgrade = true
		// if any s3 container changed version - do not prepare for s3
		for _, container := range s3Containers {
			if container.Spec.Image == cluster.Spec.Image {
				prepareForUpgrade = false
			}
		}
		if prepareForUpgrade {
			err := r.prepareForUpgradeS3(ctx, s3Containers, cluster.Spec.Image)
			if err != nil {
				return err
			}
		}

		uController = NewUpgradeController(r.getClient(), s3Containers, cluster.Spec.Image)
		err = uController.RollingUpgrade(ctx)
		if err != nil {
			return err
		}

		err = r.finalizeUpgrade(ctx, driveContainers)
		if err != nil {
			return err
		}

		cluster.Status.LastAppliedImage = cluster.Spec.Image
		if err := r.getClient().Status().Update(ctx, cluster); err != nil {
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeDrives(ctx context.Context, containers []*wekav1alpha1.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeDrives")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	// cut out everything before `:` in the image
	targetVersion = strings.Split(targetVersion, ":")[1]
	// cut out everything post-`` in the version
	// TODO: Test if this work with `-`-less versions
	targetVersion = strings.Split(targetVersion, "-")[0]

	cmd := `
wekaauthcli debug jrpc prepare_leader_for_upgrade
wekaauthcli debug jrpc upgrade_phase_start target_phase_type=DrivePhase target_version_name=` + targetVersion + `
`

	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeDrives", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeCompute(ctx context.Context, containers []*wekav1alpha1.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeCompute")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli status --json | grep upgrade_phase | grep -i compute ||  wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli debug jrpc upgrade_phase_start target_phase_type=ComputeRollingPhase target_version_name=` + targetVersion + `
`

	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeCompute", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) prepareForUpgradeS3(ctx context.Context, containers []*wekav1alpha1.WekaContainer, targetVersion string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "prepareForUpgradeS3")
	defer end()

	if len(containers) == 0 {
		logger.Info("No S3 containers found to ugprade")
		return nil
	}

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli debug jrpc upgrade_phase_start target_phase_type=FrontendPhase target_version_name=` + targetVersion + `
`
	_, stderr, err := executor.ExecNamed(ctx, "PrepareForUpgradeS3", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to prepare for upgrade: %s", stderr.String())
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) finalizeUpgrade(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "finalizeUpgrade")
	defer end()

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Failed to create executor")
		return nil
	}

	cmd := `
wekaauthcli debug jrpc upgrade_phase_finish
wekaauthcli debug jrpc unprepare_leader_for_upgrade
`
	stdout, stderr, err := executor.ExecNamed(ctx, "FinalizeUpgrade", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to finalize upgrade: STDERR: %s \n STDOUT:%s ", stderr.String(), stdout.String())
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) AllocateResources(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "AllocateResources")
	defer end()

	// Fetch all own containers
	// Filter by .Allocated == nil
	// TODO: Figure out if this filtering can be done by indexing, if not - rely on labels filtering, updating spec
	// Allocate resources for all containers at once, log-report for failed
	toAllocate := []*wekav1alpha1.WekaContainer{}
	for _, container := range r.containers {
		if container.Status.NodeAffinity == "" {
			continue
		}
		if container.Status.Allocations == nil {
			// Allocate resources
			toAllocate = append(toAllocate, container)
		}
	}

	if len(toAllocate) == 0 {
		// No containers to allocate resources for
		return nil
	}

	resourceAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		return err
	}

	err = resourceAllocator.AllocateContainers(ctx, r.cluster, toAllocate)
	if err != nil {
		if failedAllocs, ok := err.(*allocator.FailedAllocations); ok {
			logger.Error(err, "Failed to allocate resources for containers", "failed", len(*failedAllocs))
			// we proceed despite failures, as partial might be sufficient(?)
		} else {
			return err
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) HasS3Containers() bool {
	cluster := r.cluster

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return false
	}
	if template.S3Containers == 0 {
		return false
	}

	for _, container := range r.containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			return true
		}
	}
	return false
}

func (r *wekaClusterReconcilerLoop) HasNfsContainers() bool {
	return len(r.SelectNfsContainers(r.containers)) > 0
}

func (r *wekaClusterReconcilerLoop) MarkAsReady(ctx context.Context) error {
	wekaCluster := r.cluster

	if wekaCluster.Status.Status != wekav1alpha1.WekaClusterStatusReady {
		wekaCluster.Status.Status = wekav1alpha1.WekaClusterStatusReady
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""
		return r.getClient().Status().Update(ctx, wekaCluster)
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) refreshContainersJoinIps(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	containers := r.containers
	cluster := r.cluster

	_, err := services.ClustersJoinIps.GetJoinIps(ctx, cluster.Name, cluster.Namespace)
	if err != nil {
		logger.Debug("Failed to get join ips", "error", err)
		err := services.ClustersJoinIps.RefreshJoinIps(ctx, containers, cluster)
		if err != nil {
			logger.Error(err, "Failed to refresh join ips")
			return err
		}
	}
	return nil
}

func tickCounter(counters *sync.Map, key string, value int64) {
	val, ok := counters.Load(key)
	if ok {
		ptr := val.(int64)
		ptr += value
		counters.Store(key, ptr)
	} else {
		counters.Store(key, value)
	}
}

func getCounter(counters *sync.Map, key string) int64 {
	val, ok := counters.Load(key)
	if ok {
		ptr := val.(int64)
		return ptr
	}
	return 0
}

func (r *wekaClusterReconcilerLoop) UpdateContainersCounters(ctx context.Context) error {
	cluster := r.cluster
	containers := r.containers
	roleCreatedCounts := &sync.Map{}
	roleActiveCounts := &sync.Map{}
	driveCreatedCounts := &sync.Map{}
	maxCpu := map[string]float64{}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &wekav1alpha1.ClusterMetrics{}
	}

	// Idea to bubble up from containers, TODO: Actually use metrics and not accumulated status
	for _, container := range containers {
		// count Active containers
		if container.Status.Status == ContainerStatusRunning {
			tickCounter(roleActiveCounts, container.Spec.Mode, 1)
		}
		// count Created containers
		if container.Status.Status != PodStatePodNotRunning {
			tickCounter(roleCreatedCounts, container.Spec.Mode, 1)
			if container.Spec.Mode == wekav1alpha1.WekaContainerModeDrive {
				tickCounter(driveCreatedCounts, container.Spec.Mode, int64(max(container.Spec.NumDrives, 1)))
			}
		}

		if container.Status.Stats == nil {
			continue
		}

		if container.Status.Stats.CpuUsage.GetValue() > 0.0 {
			if container.Status.Stats.CpuUsage.GetValue() > maxCpu[container.Spec.Mode] {
				maxCpu[container.Spec.Mode] = container.Status.Stats.CpuUsage.GetValue()
			}
		}
	}

	// calculate desired counts
	cluster.Status.Stats.Containers.Compute.Containers.Desired = wekav1alpha1.IntMetric(template.ComputeContainers)
	cluster.Status.Stats.Containers.Drive.Containers.Desired = wekav1alpha1.IntMetric(template.DriveContainers)
	if template.S3Containers != 0 {
		if cluster.Status.Stats.Containers.S3 == nil {
			cluster.Status.Stats.Containers.S3 = &wekav1alpha1.ContainerMetrics{}
		}
		cluster.Status.Stats.Containers.S3.Containers.Desired = wekav1alpha1.IntMetric(template.S3Containers)
	}
	if template.NfsContainers != 0 {
		if cluster.Status.Stats.Containers.Nfs == nil {
			cluster.Status.Stats.Containers.Nfs = &wekav1alpha1.ContainerMetrics{}
		}
		cluster.Status.Stats.Containers.Nfs.Containers.Desired = wekav1alpha1.IntMetric(template.NfsContainers)
	}

	cluster.Status.Stats.Drives.DriveCounters.Desired = wekav1alpha1.IntMetric(template.DriveContainers * template.NumDrives)

	// convert to new metrics accessor
	cluster.Status.Stats.Containers.Compute.Processes.Desired = wekav1alpha1.IntMetric(max(int64(template.ComputeCores), 1) * int64(template.ComputeContainers))
	cluster.Status.Stats.Containers.Drive.Processes.Desired = wekav1alpha1.IntMetric(max(int64(template.DriveCores), 1) * int64(template.DriveContainers))

	// propagate "created" counters
	cluster.Status.Stats.Containers.Compute.Containers.Created = wekav1alpha1.IntMetric(getCounter(roleCreatedCounts, wekav1alpha1.WekaContainerModeCompute))
	cluster.Status.Stats.Containers.Drive.Containers.Created = wekav1alpha1.IntMetric(getCounter(roleCreatedCounts, wekav1alpha1.WekaContainerModeDrive))

	// propagate "active" counters
	cluster.Status.Stats.Containers.Compute.Containers.Active = wekav1alpha1.IntMetric(getCounter(roleActiveCounts, wekav1alpha1.WekaContainerModeCompute))
	cluster.Status.Stats.Containers.Drive.Containers.Active = wekav1alpha1.IntMetric(getCounter(roleActiveCounts, wekav1alpha1.WekaContainerModeDrive))
	if template.S3Containers != 0 {
		cluster.Status.Stats.Containers.S3.Containers.Active = wekav1alpha1.IntMetric(getCounter(roleActiveCounts, wekav1alpha1.WekaContainerModeS3))
	}
	if template.NfsContainers != 0 {
		cluster.Status.Stats.Containers.Nfs.Containers.Active = wekav1alpha1.IntMetric(getCounter(roleActiveCounts, wekav1alpha1.WekaContainerModeNfs))
	}

	// fill in utilization
	if maxCpu[wekav1alpha1.WekaContainerModeCompute] > 0 {
		cluster.Status.Stats.Containers.Compute.CpuUtilization = wekav1alpha1.NewFloatMetric(maxCpu[wekav1alpha1.WekaContainerModeCompute])
	}

	if maxCpu[wekav1alpha1.WekaContainerModeDrive] > 0 {
		cluster.Status.Stats.Containers.Drive.CpuUtilization = wekav1alpha1.NewFloatMetric(maxCpu[wekav1alpha1.WekaContainerModeDrive])
	}

	if maxCpu[wekav1alpha1.WekaContainerModeS3] > 0 {
		cluster.Status.Stats.Containers.S3.CpuUtilization = wekav1alpha1.NewFloatMetric(maxCpu[wekav1alpha1.WekaContainerModeS3])
	}

	// prepare printerColumns
	cluster.Status.PrinterColumns.ComputeContainers = wekav1alpha1.StringMetric(cluster.Status.Stats.Containers.Compute.Containers.String())
	cluster.Status.PrinterColumns.DriveContainers = wekav1alpha1.StringMetric(cluster.Status.Stats.Containers.Drive.Containers.String())
	cluster.Status.PrinterColumns.Drives = wekav1alpha1.StringMetric(cluster.Status.Stats.Drives.DriveCounters.String())

	if err := r.getClient().Status().Update(ctx, cluster); err != nil {
		return err
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) UpdateWekaStatusMetrics(ctx context.Context) error {
	cluster := r.cluster

	if cluster.IsTerminating() || r.cluster.Status.Status != wekav1alpha1.WekaClusterStatusReady {
		return nil
	}

	activeContainer := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, activeContainer)
	wekaStatus, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "Failed to get Weka status")
	}

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &wekav1alpha1.ClusterMetrics{}
	}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	cluster.Status.Stats.Containers.Compute.Processes.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Computes.Active * template.ComputeCores))
	cluster.Status.Stats.Containers.Drive.Processes.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Drives.Active * template.DriveCores))
	cluster.Status.Stats.Drives.DriveCounters.Active = wekav1alpha1.IntMetric(int64(wekaStatus.Drives.Active))

	cluster.Status.Stats.Containers.Compute.Processes.Created = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Computes.Total * template.ComputeCores))
	cluster.Status.Stats.Containers.Drive.Processes.Created = wekav1alpha1.IntMetric(int64(wekaStatus.Containers.Drives.Total * template.DriveCores))
	// TODO: might be incorrect with bad drives, and better to buble up from containers
	cluster.Status.Stats.Drives.DriveCounters.Created = wekav1alpha1.IntMetric(int64(wekaStatus.Drives.Total))
	//TODO: this should go via template builder and not direct dynamic access
	//if cluster.Spec.Dynamic.S3Containers != 0 {
	//TODO: S3 cant be implemented this way, should buble up from containers instead(for all)
	//}

	cluster.Status.Stats.IoStats.Throughput.Read = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.SumBytesRead))
	cluster.Status.Stats.IoStats.Throughput.Write = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.SumBytesWritten))
	cluster.Status.Stats.IoStats.Iops.Read = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumReads))
	cluster.Status.Stats.IoStats.Iops.Write = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumWrites))
	cluster.Status.Stats.IoStats.Iops.Metadata = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumOps - wekaStatus.Activity.NumReads - wekaStatus.Activity.NumWrites))
	cluster.Status.Stats.IoStats.Iops.Total = wekav1alpha1.IntMetric(int64(wekaStatus.Activity.NumOps))

	tpsRead := util2.HumanReadableThroughput(wekaStatus.Activity.SumBytesRead)
	tpsWrite := util2.HumanReadableThroughput(wekaStatus.Activity.SumBytesWritten)
	cluster.Status.PrinterColumns.Throughput = wekav1alpha1.StringMetric(fmt.Sprintf("%s/%s", tpsRead, tpsWrite))

	iopsRead := util2.HumanReadableIops(wekaStatus.Activity.NumReads)
	iopsWrite := util2.HumanReadableIops(wekaStatus.Activity.NumWrites)
	iopsOther := util2.HumanReadableIops(wekaStatus.Activity.NumOps - wekaStatus.Activity.NumReads - wekaStatus.Activity.NumWrites)
	cluster.Status.PrinterColumns.Iops = wekav1alpha1.StringMetric(fmt.Sprintf("%s/%s/%s", iopsRead, iopsWrite, iopsOther))
	cluster.Status.Stats.LastUpdate = metav1.NewTime(time.Now())

	if err := r.getClient().Status().Update(ctx, cluster); err != nil {
		return err
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureClusterMonitoringService(ctx context.Context) error {
	// TODO: Re-wrap as operation
	labels := map[string]string{
		"app":                "weka-cluster-monitoring",
		"weka.io/cluster-id": string(r.cluster.GetUID()),
	}

	annototations := map[string]string{
		// prometheus annotations
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   "80",
		"prometheus.io/path":   "/metrics",
	}

	deployment := apps.Deployment{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "monitoring-" + r.cluster.Name,
			Namespace: r.cluster.Namespace,
			Labels:    labels,
		},
		Spec: apps.DeploymentSpec{
			Replicas: util2.Int32Ref(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":                "weka-cluster-monitoring",
					"weka.io/cluster-id": string(r.cluster.GetUID()),
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: ctrl.ObjectMeta{
					Labels:      labels,
					Annotations: annototations,
				},
				Spec: v1.PodSpec{
					ImagePullSecrets: []v1.LocalObjectReference{
						{Name: r.cluster.Spec.ImagePullSecret},
					},
					Tolerations:  util.ExpandTolerations([]v1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
					NodeSelector: r.cluster.Spec.NodeSelector, //TODO: Monitoring-specific node-selector
					Containers: []v1.Container{
						{
							Name:  "weka-cluster-metrics",
							Image: config.Config.Metrics.Clusters.Image,
							Ports: []v1.ContainerPort{
								{
									ContainerPort: 80,
								},
							},
							Command: []string{
								"/bin/sh",
								"-c",
								`echo 'server {
								listen 80;
								location / {
									root /data;
									autoindex on;
									default_type text/plain;
								}
							}' > /etc/nginx/conf.d/default.conf &&
							nginx -g 'daemon off;'`,
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "data",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
	err := ctrl.SetControllerReference(r.cluster, &deployment, r.Manager.GetScheme())
	if err != nil {
		return err
	}

	upsert := func() error {
		err = r.getClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
		if err == nil {
			// already exists, no need to stress api with creates
			return nil
		}

		err = r.getClient().Create(ctx, &deployment)
		if err != nil {
			if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
				//fetch current deployment
				err = r.getClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		return nil
	}

	if err := upsert(); err != nil {
		return err
	}

	// we should have deployment at hand now
	kubeService := kubernetes.NewKubeService(r.getClient())
	// searching for own pods
	pods, err := kubeService.GetPodsSimple(ctx, r.cluster.Namespace, "", labels)
	if err != nil {
		return err
	}
	// find running pod
	var pod *v1.Pod
	for _, p := range pods {
		if p.Status.Phase == v1.PodRunning {
			pod = &p
			break
		}
	}
	if pod == nil {
		return lifecycle.NewWaitError(errors.New("No running monitoring pod found"))
	}

	exec, err := util2.NewExecInPodByName(r.RestClient, r.Manager.GetConfig(), pod, "weka-cluster-metrics")
	if err != nil {
		return err
	}
	data, err := resources.BuildClusterPrometheusMetrics(r.cluster)
	if err != nil {
		return err
	}
	// finally write a data
	// better data operation/labeling to come
	cmd := []string{
		"/bin/sh",
		"-ec",
		`cat <<EOF > /data/metrics.tmp
` + data + `
EOF
mv /data/metrics.tmp /data/metrics
`,
	}
	stdout, stderr, err := exec.ExecNamed(ctx, "WriteMetrics", cmd)
	if err != nil {
		return errors.Wrapf(err, "Failed to write metrics: %s\n%s", stderr.String(), stdout.String())
	}
	return nil
}

func BuildMissingContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster, template allocator.ClusterTemplate, existingContainers []*wekav1alpha1.WekaContainer) ([]*wekav1alpha1.WekaContainer, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "BuildMissingContainers")
	defer end()

	containers := make([]*wekav1alpha1.WekaContainer, 0)

	for _, role := range []string{"drive", "compute", "s3", "envoy", "nfs"} {
		var numContainers int

		switch role {
		case "compute":
			numContainers = template.ComputeContainers
		case "drive":
			numContainers = template.DriveContainers
		case "s3":
			numContainers = template.S3Containers
		case "envoy":
			numContainers = template.S3Containers
		case "nfs":
			numContainers = template.NfsContainers
		}

		currentCount := 0
		for _, container := range existingContainers {
			if container.Spec.Mode == role {
				currentCount++
			}
		}

		for i := currentCount; i < numContainers; i++ {
			name := allocator.NewContainerName(role)
			logger.Info("Building missing container", "role", role, "name", name)

			container, err := factory.NewWekaContainerForWekaCluster(cluster, template, role, name)
			if err != nil {
				logger.Error(err, "Failed to build container", "role", role, "name", name)
				return nil, err
			}
			containers = append(containers, container)
		}
	}
	return containers, nil
}

type UpdatableClusterSpec struct {
	AdditionalMemory   wekav1alpha1.AdditionalMemory
	Tolerations        []string
	RawTolerations     []v1.Toleration
	DriversDistService string
	ImagePullSecret    string
}

func NewUpdatableClusterSpec(spec *wekav1alpha1.WekaClusterSpec) *UpdatableClusterSpec {
	return &UpdatableClusterSpec{
		AdditionalMemory:   spec.AdditionalMemory,
		Tolerations:        spec.Tolerations,
		RawTolerations:     spec.RawTolerations,
		DriversDistService: spec.DriversDistService,
		ImagePullSecret:    spec.ImagePullSecret,
	}
}
