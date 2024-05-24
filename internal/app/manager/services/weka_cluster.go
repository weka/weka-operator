package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"

	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WekaClusterService interface {
	Create(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	GetCluster() *wekav1alpha1.WekaCluster
	FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	EnsureNoS3Containers(ctx context.Context) error
	GetOwnedContainers(ctx context.Context, mode string) ([]*wekav1alpha1.WekaContainer, error)
	EnsureClusterContainerIds(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
	GetUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error)
	ApplyClusterCredentials(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error
}

func NewWekaClusterService(mgr ctrl.Manager, cluster *wekav1alpha1.WekaCluster) WekaClusterService {
	client := mgr.GetClient()
	config := mgr.GetConfig()
	return &wekaClusterService{
		Client:      client,
		ExecService: NewExecService(config),
		Cluster:     cluster,
	}
}

type wekaClusterService struct {
	Client      client.Client
	ExecService ExecService

	Cluster *wekav1alpha1.WekaCluster
}

type WekaClusterServiceError struct {
	werrors.WrappedError
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
	// cmd := fmt.Sprintf("weka status || weka cluster create %s --host-ips %s", strings.Join(hostnamesList, " "), hostIpsStr) // In general not supposed to pass join secret here, but it is broken on weka. Preserving this line for quick comment/uncomment cycles
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

func (r *wekaClusterService) FormCluster(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
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
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeEnvoy {
			continue
		}
		if container.Spec.Ipv6 {
			hostIps = append(hostIps, fmt.Sprintf("[%s]:%d", container.Status.ManagementIP, container.Spec.Port))
		} else {
			hostIps = append(hostIps, fmt.Sprintf("%s:%d", container.Status.ManagementIP, container.Spec.Port))
		}
		hostnamesList = append(hostnamesList, container.Status.ManagementIP)
	}

	cmd := "weka cluster hot-spare 0"
	logger.Debug("Disabling hot spare")
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
	clusterName := r.Cluster.ObjectMeta.Name
	cmd = fmt.Sprintf("weka cluster update --cluster-name %s", clusterName)
	logger.Debug("Updating cluster name")
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetName", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
	}

	//cmd = fmt.Sprintf("weka cluster hot-spare 0")
	//logger.Debug("Disabling hot spare")
	//_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetHotSpare", []string{"bash", "-ce", cmd})
	//if err != nil {
	//	return errors.Wrapf(err, "Failed to disable hot spare: %s", stderr.String())
	//}

	if err := r.Client.Status().Update(ctx, r.Cluster); err != nil {
		// return errors.Wrap(err, "Failed to update wekaCluster status")
		return &WekaClusterServiceError{
			WrappedError: werrors.WrappedError{Err: err},
			Cluster:      r.Cluster,
		}
	}

	logger.SetPhase("Cluster created")
	return nil
}

func (r *wekaClusterService) GetOwnedContainers(ctx context.Context, mode string) ([]*wekav1alpha1.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClusterContainers", "cluster", r.Cluster.Name, "mode", mode, "cluster_uid", string(r.Cluster.UID))
	defer end()

	containersList := wekav1alpha1.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.Cluster.Namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(r.Cluster.UID)},
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}
	err := r.Client.List(ctx, &containersList, listOpts...)
	logger.InfoWithStatus(codes.Ok, "Listed containers", "count", len(containersList.Items))

	if err != nil {
		return nil, err
	}

	containers := []*wekav1alpha1.WekaContainer{}
	for i := range containersList.Items {
		containers = append(containers, &containersList.Items[i])
	}
	return containers, nil
}

func (r *wekaClusterService) EnsureNoS3Containers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureNoS3Containers", "cluster", r.Cluster.Name)
	defer end()

	containers, err := r.GetOwnedContainers(ctx, "s3")
	if err != nil {
		logger.Error(err, "Failed to get owned containers")
		return err
	}

	for _, container := range containers {
		// delete object
		// check if not already being deleted
		if container.GetDeletionTimestamp() != nil {
			continue
		}
		err := r.Client.Delete(ctx, container)
		if err != nil {
			logger.Error(err, "Failed to delete container", "container", container.Name)
			return err
		}
	}
	if len(containers) == 0 {
		logger.SetStatus(codes.Ok, "No S3 containers found")
		return nil
	} else {
		return errors.New("Waiting for all containers to stop")
	}
}

func (r *wekaClusterService) EnsureClusterContainerIds(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	var containersMap resources.ClusterContainersMap
	cluster := r.GetCluster()
	container := cluster.SelectActiveContainer(ctx, containers, wekav1alpha1.WekaContainerModeDrive)
	if container == nil {
		container = containers[0] // a fallback if we have none active, this is most surely initial clusterform
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureClusterContainerIds", "container_name", container.Name)
	defer end()

	fetchContainers := func() error {
		pod, err := resources.NewContainerFactory(container).Create(ctx)
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
		executor, err := util.NewExecInPod(clusterizePod)
		if err != nil {
			return errors.Wrap(err, "Could not create executor")
		}
		cmd := "weka cluster container -J"
		if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterSecretsApplied) {
			cmd = "wekaauthcli cluster container -J"
		}
		stdout, stderr, err := executor.ExecNamed(ctx, "WekaClusterContainer", []string{"bash", "-ce", cmd})
		if err != nil {
			logger.Error(err, "Failed to fetch containers list from cluster", "stderr, ", stderr.String(), "container", container.Name)
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
				logger.Error(err, "Container not found in cluster", "container_name", container.Name)
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

func (r *wekaClusterService) GetUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error) {
	secret := &v1.Secret{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		return "", "", err
	}
	username := secret.Data["username"]
	password := secret.Data["password"]
	return string(username), string(password), nil
}

func (r *wekaClusterService) ApplyClusterCredentials(ctx context.Context, containers []*wekav1alpha1.WekaContainer) error {
	wekaService := NewWekaService(r.ExecService, containers[0])

	cluster := r.GetCluster()
	credentials := []string{
		cluster.GetOperatorSecretName(),
		cluster.GetUserSecretName(),
	}
	for _, secretName := range credentials {
		username, password, err := r.GetUsernameAndPassword(ctx, cluster.Namespace, secretName)
		if err != nil {
			return err
		}
		if err = wekaService.EnsureUser(ctx, username, password, "clusteradmin"); err != nil {
			return err
		}
	}

	if err := wekaService.EnsureNoUser(ctx, "admin"); err != nil {
		return err
	}

	return nil
}
