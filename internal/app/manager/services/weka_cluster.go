package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/factories"
	"github.com/weka/weka-operator/internal/pkg/common"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
)

type WekaClusterService interface {
	EnsureClusterContainerIds(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error
	EnsureDefaultFs(ctx context.Context, container *wekav1alpha1.WekaContainer) error
	EnsureS3Cluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	EnsureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error)
	IsContainersReady(ctx context.Context, containers []*wekav1alpha1.WekaContainer) (bool, error)
	StartIo(ctx context.Context, wekaCluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error
}

func NewWekaClusterService(mgr ctrl.Manager, cluster *wekav1alpha1.WekaCluster) WekaClusterService {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	scheme := mgr.GetScheme()
	return &wekaClusterService{
		Client:      client,
		ExecService: NewExecService(config),
		Cluster:     cluster,

		Scheme:               scheme,
		AllocationService:    NewAllocationService(client),
		CredentialsService:   NewCredentialsService(client, scheme, config),
		WekaContainerFactory: factories.NewWekaContainerFactory(scheme),
	}
}

type wekaClusterService struct {
	Client client.Client
	Scheme *runtime.Scheme

	AllocationService    AllocationService
	CredentialsService   CredentialsService
	ExecService          ExecService
	WekaContainerFactory factories.WekaContainerFactory

	Cluster *wekav1alpha1.WekaCluster
}

func (r *wekaClusterService) GetCluster() *wekav1alpha1.WekaCluster {
	return r.Cluster
}

func (r *wekaClusterService) Create(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "createCluster", "cluster", r.Cluster.Name, "containers", len(containers))
	defer end()

	if len(containers) == 0 {
		err := pretty.Errorf("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	var hostIps []string
	var hostnamesList []string

	for _, container := range containers {
		hostIps = append(hostIps, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.Spec.Port))
		hostnamesList = append(hostnamesList, container.Status.ManagementIP)
	}
	hostIpsStr := strings.Join(hostIps, ",")
	//cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr) // In general not supposed to pass join secret here, but it is broken on weka. Preserving this line for quick comment/uncomment cycles
	cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s --join-secret=`cat /var/run/secrets/weka-operator/operator-user/join-secret`", strings.Join(hostnamesList, " "), hostIpsStr)
	logger.Info("Creating cluster", "cmd", cmd)

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Could not create executor")
		return errors.Wrap(err, "Could not create executor")
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaStatusOrWekaClusterCreate", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Failed to create cluster")
		return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
	}
	logger.Info("Cluster created", "stdout", stdout.String(), "stderr", stderr.String())

	// update cluster name
	clusterName := r.Cluster.GetUID()
	cmd = fmt.Sprintf("weka cluster update --cluster-name %s", clusterName)
	logger.Debug("Updating cluster name")
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetName", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
	}

	if err := r.Client.Status().Update(ctx, r.Cluster); err != nil {
		return errors.Wrap(err, "Failed to update wekaCluster status")
	}
	logger.SetPhase("Cluster created")
	return nil
}

func (r *wekaClusterService) EnsureDefaultFs(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDefaultFs")
	defer end()

	wekaService := NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	err = wekaService.CreateFilesystemGroup(ctx, "default")
	if err != nil {
		if !errors.As(err, &FilesystemGroupExists{}) {
			return err
		}
	}

	// This defaults are not meant to be configurable, as instead weka should not require them.
	// Until then, user configuratino post cluster create

	thinProvisionedLimits := status.Capacity.TotalBytes / 2 // half a total capacity allocated for thin provisioning
	const s3ReservedCapacity = 100 * 1024 * 1024 * 1024
	var configFsSize int64 = 3 * 1024 * 1024 * 1024

	err = wekaService.CreateFilesystem(ctx, ".config_fs", "default", FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimits, 10),
		ThickProvisioningCapacity: strconv.FormatInt(configFsSize, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		if !errors.As(err, &FilesystemExists{}) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default-s3", "default", FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimits, 10),
		ThickProvisioningCapacity: strconv.FormatInt(s3ReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		if !errors.As(err, &FilesystemExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "default filesystem ensured")
	return nil
}

func (r *wekaClusterService) EnsureS3Cluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureS3Cluster")
	defer end()

	if len(containers) == 0 {
		err := &common.ArgumentError{
			Argument: "containers",
			Message:  "containers list is empty",
		}

		logger.Error(err, "containers list is empty")
		return err
	}

	container := containers[0]
	wekaService := NewWekaService(r.ExecService, container)
	containerIds := []int{}
	for i, c := range containers {

		if c.Status.ClusterContainerID == nil {
			arg := fmt.Sprintf("containers[%d].Status.ClusterContainerID", i)
			err := &common.ArgumentError{
				Argument: arg,
				Message:  "invalid container status",
			}
			logger.Error(err, "nil container id")
			return err
		}

		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	if container.Spec.S3Params == nil {
		err := &common.ArgumentError{
			Argument: "container.Spec.S3Params",
			Message:  "nil S3Params",
		}
		logger.Error(err, "nil S3Params")
		return err
	}

	err := wekaService.CreateS3Cluster(ctx, S3Params{
		EnvoyPort:      container.Spec.S3Params.EnvoyPort,
		EnvoyAdminPort: container.Spec.S3Params.EnvoyAdminPort,
		S3Port:         container.Spec.S3Params.S3Port,
		ContainerIds:   containerIds,
	})
	if err != nil {
		if !errors.As(err, &S3ClusterExists{}) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "S3 cluster ensured")
	return nil
}

func (r *wekaClusterService) EnsureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureClientsWekaContainers")
	defer end()

	allocations, allocConfigMap, err := r.AllocationService.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "could not init allocmap")
		return nil, err
	}

	foundContainers := []*wekav1alpha1.WekaContainer{}
	template := domain.WekaClusterTemplates[cluster.Spec.Template]
	topologyFn, ok := domain.Topologies[cluster.Spec.Topology]
	if !ok {
		err := pretty.Errorf("Topology %s not found", cluster.Spec.Topology)
		logger.Error(err, "Failed to get topology")
		return nil, err
	}

	topology, err := topologyFn(ctx, r.Client, cluster.Spec.NodeSelector)
	allocator := domain.NewAllocator(topology)
	if err != nil {
		logger.Error(err, "Failed to get topology", "topology", cluster.Spec.Topology)
		return nil, err
	}
	allocations, err, changed := allocator.Allocate(
		ctx,
		domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
		template,
		allocations,
		cluster.Spec.Size)
	if err != nil {
		logger.Error(err, "Failed to allocate resources")
		return nil, err
	}
	if changed {
		if err := r.AllocationService.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
			return nil, err
		}
	}

	size := cluster.Spec.Size
	if size == 0 {
		size = 1
	}
	logger.InfoWithStatus(codes.Unset, "Ensuring containers")

	ensureContainers := func(role string, containersNum int) error {
		for i := 0; i < containersNum; i++ {
			ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureContainers")
			// Check if the WekaContainer object exists
			owner := domain.Owner{
				OwnerCluster: domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
				Container:    fmt.Sprintf("%s%d", role, i),
				Role:         role,
			} // apparently need helper function with a role.

			ownedResources, _ := domain.GetOwnedResources(owner, allocations)
			wekaContainer, err := r.WekaContainerFactory.NewForCluster(cluster, ownedResources, template, topology, role, i)
			if err != nil {
				logger.Error(err, "Failed to create WekaContainer")
				end()
				return err
			}
			l := logger.WithValues("container_name", wekaContainer.Name)

			found := &wekav1alpha1.WekaContainer{}
			err = r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: wekaContainer.Name}, found)
			if err != nil && apierrors.IsNotFound(err) {
				// Define a new WekaContainer object
				l.Info("Creating container")
				err = r.Client.Create(ctx, wekaContainer)
				if err != nil {
					logger.Error(err, "Failed to create WekaContainer")
					end()
					return err
				}
				foundContainers = append(foundContainers, wekaContainer)
				l.Info("Container created")
			} else {
				foundContainers = append(foundContainers, found)
				l.Info("Container already exists")
			}
			end()
		}
		return nil
	}
	if err := ensureContainers("drive", template.DriveContainers); err != nil {
		logger.Error(err, "Failed to ensure drive containers")
		return nil, err
	}
	if err := ensureContainers("compute", template.ComputeContainers); err != nil {
		logger.Error(err, "Failed to ensure compute containers")
		return nil, err
	}
	logger.InfoWithStatus(codes.Ok, "All cluster containers are created")
	return foundContainers, nil
}

func (r *wekaClusterService) EnsureClusterContainerIds(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureClusterContainerIds", "cluster", cluster.Name)
	defer end()
	var containersMap resources.ClusterContainersMap

	fetchContainers := func() error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "FetchContainers")
		defer end()
		logger.Info("Fetching containers list from cluster")

		pod, err := resources.NewContainerFactory(containers[0]).Create(ctx)
		if err != nil {
			logger.Error(err, "Could not find executor pod")
			return err
		}
		clusterizePod := &v1.Pod{}
		err = r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: pod.Name}, clusterizePod)
		if err != nil {
			logger.Error(err, "Could not find clusterize pod")
			return err
		}
		// executor, err := util.NewExecInPod(clusterizePod)
		executor, err := r.ExecService.GetExecutor(ctx, containers[0])
		if err != nil {
			return errors.Wrap(err, "Could not create executor")
		}
		cmd := "weka cluster container -J"
		stdout, stderr, err := executor.ExecNamed(ctx, "WekaClusterContainer", []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to fetch containers list from cluster")
		}
		response := resources.ClusterContainersResponse{}
		err = json.Unmarshal(stdout.Bytes(), &response)
		if err != nil {
			return errors.Wrapf(err, "Failed to create cluster: %s", stderr.String())
		}
		containersMap, err = resources.MapByContainerName(response)
		if err != nil {
			return errors.Wrapf(err, "Failed to map containers")
		}
		return nil
	}

	for _, container := range containers {
		if container.Status.ClusterContainerID == nil {
			if containersMap == nil {
				err := fetchContainers()
				if err != nil {
					logger.Error(err, "Failed to fetch containers list from cluster")
					return err
				}
			}

			if clusterContainer, ok := containersMap[container.Spec.WekaContainerName]; !ok {
				err := errors.New("Container " + container.Spec.WekaContainerName + " not found in cluster")
				logger.Error(err, "Container not found in cluster", "container_name", container.Spec.WekaContainerName)
				logger.Info("Containers list", "containers", containersMap)
				return err
			} else {
				containerId, err := clusterContainer.ContainerId()
				if err != nil {
					return errors.Wrap(err, "Failed to parse container id")
				}
				container.Status.ClusterContainerID = &containerId
				if err := r.Client.Status().Update(ctx, container); err != nil {
					return errors.Wrap(err, "Failed to update container status")
				}
			}
		}
	}
	logger.InfoWithStatus(codes.Ok, "Cluster container ids are set")
	return nil
}

func (r *wekaClusterService) IsContainersReady(ctx context.Context, containers []*wekav1alpha1.WekaContainer) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "IsContainersReady")
	defer end()

	for _, container := range containers {
		ready, err := container.IsReady(ctx)
		if err != nil {
			logger.Error(err, "Failed to check if container is ready")
			return false, err
		}
		if !ready {
			logger.Info("Container is not ready", "container_name", container.Name)
			return false, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Containers are ready")
	return true, nil
}

func (r *wekaClusterService) StartIo(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "StartIo")
	defer end()

	if len(containers) == 0 {
		err := pretty.Errorf("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	logger.SetPhase("STARTING_IO")
	cmd := "weka cluster start-io"
	_, stderr, err := executor.ExecNamed(ctx, "StartIO", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Failed to start-io")
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}
	logger.InfoWithStatus(codes.Ok, "IO started")
	logger.SetPhase("IO_STARTED")
	return nil
}
