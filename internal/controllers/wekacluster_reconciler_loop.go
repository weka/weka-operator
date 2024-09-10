package controllers

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/condition"
	"github.com/weka/weka-operator/internal/controllers/factory"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	util2 "github.com/weka/weka-operator/pkg/util"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"slices"
	"strconv"
	"strings"
	"time"
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

	currentContainers := discovery.GetClusterContainers(ctx, r.getClient(), cluster, "")
	missingContainers, err := BuildMissingContainers(cluster, template, currentContainers)
	if err != nil {
		logger.Error(err, "Failed to create missing containers")
		return err
	}
	for _, container := range missingContainers {
		if err := ctrl.SetControllerReference(cluster, container, r.Manager.GetScheme()); err != nil {
			return err
		}
	}

	r.containers = currentContainers
	if len(missingContainers) == 0 {
		return nil
	}

	resourcesAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	if err != nil {
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
			errs = append(errs, err)
			continue
		}
		allContainers = append(allContainers, container)
	}
	allContainers = append(currentContainers, allContainers...)
	r.containers = allContainers
	return nil
}

func (r *wekaClusterReconcilerLoop) allocateContainersResources() {
	//nodeInfoGetter := func(ctx context.Context, nodeName wekav1alpha1.NodeName) (*discovery.DiscoveryNodeInfo, error) {
	//	discoverNodeOp := operations.NewDiscoverNodeOperation(
	//		r.Manager,
	//		nodeName,
	//		r.cluster,
	//		r.cluster.ToOwnerObject(),
	//	)
	//	err := operations.ExecuteOperation(ctx, discoverNodeOp)
	//	if err != nil {
	//		return nil, err
	//	}
	//	return discoverNodeOp.GetResult(), nil
	//}
	//
	//newPortAllocator, err := allocator.NewResourcesAllocator(ctx, r.getClient())
	//if err != nil {
	//	logger.Error(err, "Failed to create topology allocator")
	//	return err
	//}
}

func (r *wekaClusterReconcilerLoop) getClient() client.Client {
	return r.Manager.GetClient()
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

func (r *wekaClusterReconcilerLoop) HandleDeletion(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
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

	// Preserving whole Spec for more generic approach on status, while being able to update only specific fields on containers
	specHash, err := util2.HashStruct(cluster.Spec)
	if err != nil {
		return err
	}
	if specHash != cluster.Status.LastAppliedSpec {
		for _, container := range containers {
			changed := false
			additionalMemory := cluster.Spec.GetAdditionalMemory(container.Spec.Mode)
			if container.Spec.AdditionalMemory != additionalMemory {
				container.Spec.AdditionalMemory = additionalMemory
				changed = true
			}

			tolerations := util2.ExpandTolerations([]v1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations)
			if !reflect.DeepEqual(container.Spec.Tolerations, tolerations) {
				container.Spec.Tolerations = tolerations
				changed = true
			}

			if container.Spec.DriversDistService != cluster.Spec.DriversDistService {
				container.Spec.DriversDistService = cluster.Spec.DriversDistService
				changed = true
			}

			if container.Spec.ImagePullSecret != cluster.Spec.ImagePullSecret {
				container.Spec.ImagePullSecret = cluster.Spec.ImagePullSecret
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
		if !container.IsWekaContainer() {
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

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
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
	wekaService := services.NewWekaService(r.ExecService, containers[0])

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

func (r *wekaClusterReconcilerLoop) SelectActiveContainer(containers []*wekav1alpha1.WekaContainer) *wekav1alpha1.WekaContainer {
	for _, container := range containers {
		if container.Status.ClusterContainerID != nil {
			return container
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) DeleteAdminUser(ctx context.Context) error {
	container := r.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, container)
	return wekaService.EnsureNoUser(ctx, "admin")
}

func (r *wekaClusterReconcilerLoop) EnsureDefaultFS(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDefaultFs")
	defer end()

	container := r.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	err = wekaService.CreateFilesystemGroup(ctx, "default")
	if err != nil {
		if !errors.As(err, &services.FilesystemGroupExists{}) {
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
		if !errors.As(err, &services.FilesystemExists{}) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsDefault, 10),
		ThickProvisioningCapacity: strconv.FormatInt(fsReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		if !errors.As(err, &services.FilesystemExists{}) {
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

func (r *wekaClusterReconcilerLoop) EnsureS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureS3Cluster")
	defer end()

	cluster := r.cluster
	containers := r.SelectS3Containers(r.containers)

	container := containers[0]
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

func (r *wekaClusterReconcilerLoop) applyClientLoginCredentials(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClientLoginCredentials")
	defer end()

	cluster := r.cluster
	containers := r.containers

	container := r.SelectActiveContainer(containers)
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

	container := r.SelectActiveContainer(containers)
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

	driveContainer := wekaCluster.SelectActiveContainer(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if driveContainer == nil {
		return errors.New("No drive container found")
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

	updateContainer := func(container *wekav1alpha1.WekaContainer) error {
		if container.Status.LastAppliedImage != cluster.Spec.Image {
			if container.Spec.Image != cluster.Spec.Image {
				container.Spec.Image = cluster.Spec.Image
				if err := r.getClient().Update(ctx, container); err != nil {
					return err
				}
			}
		}
		return nil
	}
	areUpgraded := func(containers []*wekav1alpha1.WekaContainer) bool {
		for _, container := range containers {
			if container.Status.LastAppliedImage != container.Spec.Image {
				return false
			}
		}
		return true
	}

	allAtOnceUpgrade := func(containers []*wekav1alpha1.WekaContainer) error {
		for _, container := range containers {
			if err := updateContainer(container); err != nil {
				return err
			}
		}
		if !areUpgraded(containers) {
			return errors.New("containers upgrade not finished yet")
		}
		return nil
	}
	_ = allAtOnceUpgrade // preserving function to return later, so weka will manage the order

	rollingUpgrade := func(containers []*wekav1alpha1.WekaContainer) error {
		for _, container := range containers {
			if container.Status.LastAppliedImage != container.Spec.Image {
				return errors.New("container upgrade not finished yet")
			}
		}

		for _, container := range containers {
			if container.Spec.Image != cluster.Spec.Image {
				err := updateContainer(container)
				if err != nil {
					return err
				}
				return errors.New("container upgrade not finished yet")
			}
		}
		return nil
	}

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

		err = rollingUpgrade(driveContainers)
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

		err = rollingUpgrade(computeContainers)
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
		err = rollingUpgrade(s3Containers)
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

	// cut out everything post-`` in the version
	targetVersion = strings.Split(targetVersion, "-")[0]
	// cut out everything before `:` in the image
	targetVersion = strings.Split(targetVersion, ":")[1]

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

func (r *wekaClusterReconcilerLoop) MarkAsReady(ctx context.Context) error {
	wekaCluster := r.cluster

	if wekaCluster.Status.Status != "Ready" {
		wekaCluster.Status.Status = "Ready"
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""
		return r.getClient().Status().Update(ctx, wekaCluster)
	}
	return nil
}

func BuildMissingContainers(cluster *wekav1alpha1.WekaCluster, template allocator.ClusterTemplate, existingContainers []*wekav1alpha1.WekaContainer) ([]*wekav1alpha1.WekaContainer, error) {
	containers := make([]*wekav1alpha1.WekaContainer, 0)

	for _, role := range []string{"drive", "compute", "s3", "envoy"} {
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
		}

		currentCount := 0
		for _, container := range existingContainers {
			if container.Spec.Mode == role {
				currentCount++
			}
		}

		for i := currentCount; i < numContainers; i++ {
			container, err := factory.NewWekaContainerForWekaCluster(cluster, template, role, wekav1alpha1.NewContainerName(role))
			if err != nil {
				return nil, err
			}
			containers = append(containers, container)
		}
	}
	return containers, nil
}
