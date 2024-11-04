package controllers

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-k8s-api/util"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	util2 "github.com/weka/weka-operator/pkg/util"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func NewWekaClusterReconcileLoop(mgr ctrl.Manager) *wekaClusterReconcilerLoop {
	config := mgr.GetConfig()
	execService := exec.NewExecService(config)
	scheme := mgr.GetScheme()
	return &wekaClusterReconcilerLoop{
		Manager:        mgr,
		ExecService:    execService,
		SecretsService: services.NewSecretsService(mgr.GetClient(), scheme, execService),
	}
}

type wekaClusterReconcilerLoop struct {
	Manager        ctrl.Manager
	ExecService    exec.ExecService
	cluster        *wekav1alpha1.WekaCluster
	clusterService services.WekaClusterService
	containers     []*wekav1alpha1.WekaContainer
	SecretsService services.SecretsService
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
	r.clusterService = services.NewWekaClusterService(r.Manager, wekaCluster)

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
		err := fmt.Errorf("Template not found")
		logger.Error(err, "Template not found", "template", cluster.Spec.Template, "keys", keys)
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
		joinIps, err = discovery.GetJoinIps(ctx, r.getClient(), cluster)
		allowExpansion := false
		if err != nil {
			allowExpansion = strings.Contains(err.Error(), "No join IP port pairs found") || strings.Contains(err.Error(), "No compute containers found")
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
	containers := r.containers

	if cluster.Status.Status != wekav1alpha1.WekaClusterStatusGracePeriod {
		cluster.Status.Status = wekav1alpha1.WekaClusterStatusGracePeriod
		err := r.getClient().Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return err
		}
	}

	for _, container := range containers {
		if container.Spec.State == wekav1alpha1.ContainerStatePaused {
			continue
		}
		container.Spec.State = wekav1alpha1.ContainerStatePaused
		err := r.getClient().Update(ctx, container)
		if err != nil {
			logger.Error(err, "Failed to update container state")
			return err
		}
	}

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)

	logger.Info("Cluster is in graceful deletion", "deletionTime", deletionTime)
	return nil
}

func (r *wekaClusterReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	gracefulDestroyDuration := r.cluster.GetGracefulDestroyDuration()
	deletionTime := r.cluster.GetDeletionTimestamp().Time.Add(gracefulDestroyDuration)
	logger.Debug("Not graceful deletion", "deletionTime", deletionTime, "now", time.Now(), "gracefulDestroyDuration", gracefulDestroyDuration)

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
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster
	clusterService := r.clusterService

	err := clusterService.EnsureNoContainers(ctx, "s3")
	if err != nil {
		return err
	}

	err = clusterService.EnsureNoContainers(ctx, "")
	if err != nil {
		return err
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

	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, containers[0], &kubeExecTimeout)
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

func (r *wekaClusterReconcilerLoop) SelectNfsGatewayContainers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var nfsContainers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeNfsGateway {
			nfsContainers = append(nfsContainers, container)
		}
	}
	return nfsContainers
}

func (r *wekaClusterReconcilerLoop) EnsureS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureS3Cluster")
	defer end()

	cluster := r.cluster
	containers := r.SelectS3Containers(r.containers)

	container := discovery.SelectActiveContainer(containers)
	wekaService := services.NewWekaService(r.ExecService, container)
	containerIds := []int{}
	for _, c := range containers {
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	err := wekaService.CreateS3Cluster(ctx, services.S3Params{
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

func (r *wekaClusterReconcilerLoop) EnsureNfsGateway(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNfsGateway")
	defer end()

	containers := r.SelectNfsGatewayContainers(r.containers)

	container := containers[0]
	wekaService := services.NewWekaService(r.ExecService, container)
	containerIds := []int{}
	for _, c := range containers {
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	err := wekaService.ConfigureNfsGateway(ctx, services.NFSParams{
		ConfigFilesystem: ".config_fs",
	})

	if err != nil {
		if !errors.As(err, &services.NfsInterfaceGroupExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "NFS Gateway ensured")
	return nil
}

func (r *wekaClusterReconcilerLoop) applyClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClientLoginCredentials")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := discovery.SelectActiveContainer(containers)
	username, password, err := r.getUsernameAndPassword(ctx, cluster.Namespace, cluster.GetClientSecretName())

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
wekaauthcli debug jrpc upgrade_phase_finish
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
	resourceAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
		return err
	}

	err = resourceAllocator.AllocateContainers(ctx, r.cluster, toAllocate)
	notChanged := []*wekav1alpha1.WekaContainer{}

	if err != nil {
		if failedAllocs, ok := err.(*allocator.FailedAllocations); ok {
			for _, allocation := range *failedAllocs {
				notChanged = append(notChanged, allocation.Container)
			}
			logger.Error(err, "Failed to allocate resources for containers", "failed", len(*failedAllocs))
			// we proceed despite failures, as partial might be sufficient(?)
		} else {
			return err
		}
	}

	// update not changed containers
	failedWrites := false
	for _, container := range toAllocate {
		if slices.Contains(notChanged, container) {
			continue
		}
		if err := r.getClient().Status().Update(ctx, container); err != nil {
			failedWrites = true
			continue
		}
	}
	if err != nil {
		return err // we might want not to return err on failed allocations, if we within resiliency limits
	}
	if failedWrites {
		return errors.New("Failed to update containers, re-reconciling")
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) HasS3Containers() bool {
	return len(r.SelectS3Containers(r.containers)) > 0
}

func (r *wekaClusterReconcilerLoop) HasNfsGatewayContainers() bool {
	return len(r.SelectNfsGatewayContainers(r.containers)) > 0
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

func getJoinIpsCondition(container *wekav1alpha1.WekaContainer) *metav1.Condition {
	for _, cond := range container.Status.Conditions {
		if cond.Type == condition.CondJoinIpsSet {
			return &cond
		}
	}
	return nil
}

func joinIpsUpdated(joinIpsCondition *metav1.Condition) bool {
	if joinIpsCondition == nil {
		return false
	}
	if joinIpsCondition.Status == metav1.ConditionTrue {
		if time.Since(joinIpsCondition.LastTransitionTime.Time) > 30*time.Minute {
			return false
		}
	}
	return true
}

func (r *wekaClusterReconcilerLoop) updateContainersJoinIps(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updateContainersJoinIps")
	defer end()

	containers := r.containers
	//TODO: Parallelize
	for _, c := range containers {
		// check if was updated in last 30 minutes, and if not - re-update
		joinIpsCondition := getJoinIpsCondition(c)
		if joinIpsUpdated(joinIpsCondition) {
			continue
		}

		newJoinIps, err := discovery.SelectJoinIps(r.containers, r.cluster.Spec.FailureDomainLabel)
		if err != nil {
			return err
		}

		logger.Info("Updating container join ips", "container", c.Name, "newJoinIps", newJoinIps, "oldJoinIps", c.Spec.JoinIps)

		if joinIpsCondition != nil && joinIpsCondition.Status == metav1.ConditionTrue {
			// set condition status to false, as otherwise LastTransitionTime will not be updated
			// and we need this separate update, because we cannot manually update LastTransitionTime
			_ = meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
				Type:   condition.CondJoinIpsSet,
				Status: metav1.ConditionFalse,
				Reason: "PeriodicUpdate",
			})
			if err := r.getClient().Status().Update(ctx, c); err != nil {
				return err
			}
		}

		// separate update for spec
		c.Spec.JoinIps = newJoinIps
		if err := r.getClient().Update(ctx, c); err != nil {
			return err
		}
		// update status with new condition
		_ = meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type:   condition.CondJoinIpsSet,
			Status: metav1.ConditionTrue,
			Reason: "PeriodicUpdate",
		})
		if err := r.getClient().Status().Update(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func BuildMissingContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster, template allocator.ClusterTemplate, existingContainers []*wekav1alpha1.WekaContainer) ([]*wekav1alpha1.WekaContainer, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "BuildMissingContainers")
	defer end()

	containers := make([]*wekav1alpha1.WekaContainer, 0)

	for _, role := range []string{"drive", "compute", "s3", "envoy", "nfs-gateway"} {
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
		case "nfs-gateway":
			numContainers = template.NfsGatewayContainers
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
