package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/codes"
)

type WekaStatusCapacity struct {
	UnprovisionedBytes int64 `json:"unprovisioned_bytes"`
	TotalBytes         int64 `json:"total_bytes"`
}

type WekaStatusResponse struct {
	Status   string             `json:"status"`
	Capacity WekaStatusCapacity `json:"capacity"`
}

type WekaFilesystem struct{}

type FSParams struct {
	TotalCapacity             string
	ThinProvisioningEnabled   bool
	ThickProvisioningCapacity string
}

type S3Params struct {
	EnvoyPort      int
	EnvoyAdminPort int
	S3Port         int
	ContainerIds   []int
}

type WekaUserResponse struct {
	// OrgId    int    `json:"org_id"`
	// PosixGid string `json:"posix_gid"`
	// PosixUid string `json:"posix_uid"`
	// Role     string `json:"role"`
	// S3Policy string `json:"s3_policy"`
	// Source   string `json:"source"`
	// Uid      string `json:"uid"`
	Username string `json:"username"`
}

type Drive struct {
	Uuid      string `json:"uuid"`
	AddedTime string `json:"added_time"`
}

type DriveListOptions struct {
	ContainerId *int `json:"container_id"`
}

type WekaService interface {
	GetWekaStatus(ctx context.Context) (WekaStatusResponse, error)
	CreateFilesystem(ctx context.Context, name, group string, params FSParams) error
	CreateFilesystemGroup(ctx context.Context, name string) error
	CreateCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error
	CreateS3Cluster(ctx context.Context, s3Params S3Params) error
	JoinS3Cluster(ctx context.Context, containerId int) error
	GenerateJoinSecret(ctx context.Context) (string, error)
	GetUsers(ctx context.Context) ([]WekaUserResponse, error)
	EnsureUser(ctx context.Context, username, password, role string) error
	EnsureNoUser(ctx context.Context, username string) error
	SetWekaHome(ctx context.Context, endpoint string) error
	ListDrives(ctx context.Context, listOptions DriveListOptions) ([]Drive, error)
	// GetFilesystemByName(ctx context.Context, name string) (WekaFilesystem, error)
	StartIo(ctx context.Context) error
}

func NewWekaService(ExecService ExecService, container *v1alpha1.WekaContainer) WekaService {
	return &CliWekaService{
		ExecService: ExecService,
		Container:   container,
	}
}

type WekaServiceError struct {
	Err       error
	Container *v1alpha1.WekaContainer
	Message   string
}

func (e WekaServiceError) Error() string {
	return fmt.Sprintf("error in weka service: %s, container: %s, cause: %v", e.Message, e.Container.Name, e.Err)
}

type WekaCliError struct {
	WekaServiceError
	Command string
	Stderr  string
}

func (e WekaCliError) Error() string {
	return fmt.Sprintf("error executing CLI command: %s, container: %s, cause: %v, command: %s, stderr: %s", e.Message, e.Container.Name, e.Err, e.Command, e.Stderr)
}

type FilesystemGroupExists struct {
	error
}

type FilesystemExists struct {
	error
}

type S3ClusterExists struct {
	error
}

type CliWekaService struct {
	ExecService ExecService
	Container   *v1alpha1.WekaContainer
}

func (c *CliWekaService) ListDrives(ctx context.Context, listOptions DriveListOptions) ([]Drive, error) {
	var drives []Drive
	filters := []string{}
	wekacli := "weka"
	if c.Container.Spec.JoinIps != nil {
		wekacli = "wekaauthcli"
	}
	cmdParts := []string{wekacli, "cluster", "drive", "--json"}
	if listOptions.ContainerId != nil {
		filters = append(filters, fmt.Sprintf("host=%d", *listOptions.ContainerId))
	}
	if len(filters) != 0 {
		cmdParts = append(cmdParts, "--filter")
		cmdParts = append(cmdParts, strings.Join(filters, ","))
	}

	err := c.RunJsonCmd(ctx, cmdParts, "ListDrives", &drives)
	if err != nil {
		return nil, err
	}
	return drives, nil
}

func (c *CliWekaService) SetWekaHome(ctx context.Context, endpoint string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SetWekaHome")
	defer end()

	executor, err := c.GetExecutor(ctx)
	if err != nil {
		logger.SetError(err, "Failed to get executor")
		return err
	}

	cmd := fmt.Sprintf("wekaauthcli cloud enable --cloud-url %s", endpoint)
	_, stderr, err := executor.ExecNamed(ctx, "SetWekaHome", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.SetError(err, "Failed to set WEKA_HOME", "stderr", stderr.String())
		return err
	}

	return nil
}

func (c *CliWekaService) EnsureNoUser(ctx context.Context, username string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureNoUser")
	defer end()

	existingUsers, err := c.GetUsers(ctx)
	if err != nil {
		logger.SetError(err, "Failed to get users")
		return err
	}

	for _, user := range existingUsers {
		if user.Username == username {
			executor, err := c.GetExecutor(ctx)
			if err != nil {
				logger.SetError(err, "Failed to get executor")
				return err
			}
			cmd := fmt.Sprintf("wekaauthcli user delete %s", username)
			_, stderr, err := executor.ExecSensitive(ctx, "RemoveUser", []string{"bash", "-ce", cmd})
			if err != nil {
				logger.SetError(err, "Failed to remove user", "stderr", stderr.String())
				return err
			}
			return nil
		}
	}
	return nil
}

func (c *CliWekaService) GetUsers(ctx context.Context) ([]WekaUserResponse, error) {
	existingUsers := []WekaUserResponse{}
	cmd := "weka user -J || wekaauthcli user -J"
	executor, err := c.GetExecutor(ctx)
	if err != nil {
		return nil, err
	}

	stdout, _, err := executor.ExecSensitive(ctx, "WekaListUsers", []string{"bash", "-ce", cmd})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(stdout.Bytes(), &existingUsers)
	if err != nil {
		return nil, err
	}
	return existingUsers, nil
}

func (c *CliWekaService) GetExecutor(ctx context.Context) (util.Exec, error) {
	return c.ExecService.GetExecutor(ctx, c.Container)
}

func (c *CliWekaService) EnsureUser(ctx context.Context, username, password, role string) error {
	existingUsers, err := c.GetUsers(ctx)
	if err != nil {
		return err
	}

	for _, user := range existingUsers {
		if user.Username == username {
			return nil
		}
	}

	executor, err := c.GetExecutor(ctx)
	if err != nil {
		return err
	}

	// A hack to handle both default user and newly created user
	// This should move to a separate function with proper valudation if we have login
	// since there are just few cases this is needed - ignoring for now
	for _, cli := range []string{"weka", "wekaauthcli"} {
		cmd := fmt.Sprintf("%s user add %s %s %s", cli, username, role, password)
		_, stderr, err := executor.ExecSensitive(ctx, "AddClusterUser", []string{"bash", "-ce", cmd})
		if err != nil {
			if strings.Contains(stderr.String(), "weka user login") {
				continue
			}
			return errors.Wrapf(err, "Failed to add user: %s", stderr.String())
		}
		return nil
	}
	return errors.New("Failed to add user")
}

func (c *CliWekaService) GenerateJoinSecret(ctx context.Context) (string, error) {
	var data string
	err := c.RunJsonCmd(ctx, []string{
		"wekaauthcli", "cluster", "join-token", "generate", "--json",
	}, "GenerateJoinSecret", &data)
	if err != nil {
		return "", err
	}
	return data, nil
}

func (c *CliWekaService) JoinS3Cluster(ctx context.Context, containerId int) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "JoinS3Cluster")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		logger.SetError(err, "Failed to get executor")
		return err
	}

	cmd := []string{
		"wekaauthcli", "s3", "cluster", "containers", "add", strconv.Itoa(containerId),
	}

	_, stderr, err := executor.ExecNamed(ctx, "JoinS3Cluster", cmd)
	if err != nil {
		logger.SetError(err, "Failed to join S3 cluster", "stderr", stderr.String())
		return err
	}

	return nil
}

func (c *CliWekaService) CreateCluster(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "CreateCluster")
	defer end()

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

	executor, err := c.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		return &WekaServiceError{
			Err:       err,
			Container: c.Container,
			Message:   "Failed to create executor",
		}
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaStatusOrWekaClusterCreate", []string{"bash", "-ce", cmd})
	if err != nil {
		return &WekaCliError{
			WekaServiceError: WekaServiceError{
				Err:       err,
				Container: c.Container,
				Message:   "Failed to create cluster",
			},
			Command: cmd,
			Stderr:  stderr.String(),
		}
	}
	logger.Info("Cluster created", "stdout", stdout.String(), "stderr", stderr.String())

	// update cluster name
	clusterName := cluster.GetUID()
	cmd = fmt.Sprintf("weka cluster update --cluster-name %s", clusterName)
	logger.Debug("Updating cluster name")
	_, stderr, err = executor.ExecNamed(ctx, "WekaClusterSetName", []string{"bash", "-ce", cmd})
	if err != nil {
		// return errors.Wrapf(err, "Failed to update cluster name: %s", stderr.String())
		return &WekaCliError{
			WekaServiceError: WekaServiceError{
				Err:       err,
				Container: c.Container,
				Message:   "Failed to update cluster name",
			},
			Command: cmd,
			Stderr:  stderr.String(),
		}
	}
	return nil
}

func (c *CliWekaService) CreateS3Cluster(ctx context.Context, s3Params S3Params) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "CreateS3Cluster")
	defer end()
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	cmd := []string{
		"wekaauthcli", "s3", "cluster", "create", "default-s3", ".config_fs",
		"--port", strconv.Itoa(s3Params.EnvoyPort),
		"--envoy-admin-port", strconv.Itoa(s3Params.EnvoyAdminPort),
		"--internal-port", strconv.Itoa(s3Params.S3Port),
		"--container", commaSeparatedInts(s3Params.ContainerIds),
		//"--container-name", s3Params.MinioContainerName,
		//"--envoy-container-name", s3Params.EnvoyContainerName,
	}

	_, stderr, err := executor.ExecNamed(ctx, "CreateS3Cluster", cmd)
	if err != nil {
		if strings.Contains(stderr.String(), "already exists") {
			return &S3ClusterExists{err}
		}
		logger.SetError(err, "Failed to create S3 cluster", "stderr", stderr.String())
		return err
	}

	return nil
}

func commaSeparatedInts(ids []int) string {
	var strIds []string
	for _, id := range ids {
		strIds = append(strIds, strconv.Itoa(id))
	}
	return strings.Join(strIds, ",")
}

func (c *CliWekaService) CreateFilesystemGroup(ctx context.Context, name string) error {
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}
	cmd := []string{
		"wekaauthcli", "fs", "group", "create", name,
	}
	_, stderr, err := executor.ExecNamed(ctx, "CreateFilesystemGroup", cmd)
	if err != nil {
		if strings.Contains(stderr.String(), "already exists") {
			return &FilesystemGroupExists{err}
		}
		return err
	}
	return nil
}

func (c *CliWekaService) CreateFilesystem(ctx context.Context, name, group string, params FSParams) error {
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}
	cmd := []string{
		"wekaauthcli", "fs", "create", name, group, params.TotalCapacity,
	}

	if params.ThinProvisioningEnabled {
		cmd = append(cmd, "--thin-provision-max-ssd", params.TotalCapacity)
		cmd = append(cmd, "--thin-provision-min-ssd", params.ThickProvisioningCapacity)
	}

	_, stderr, err := executor.ExecNamed(ctx, "CreateFilesystem", cmd)
	if err != nil {
		if strings.Contains(stderr.String(), "already exists") {
			return &FilesystemExists{err}
		}
		return err
	}
	return nil
}

func (c *CliWekaService) GetWekaStatus(ctx context.Context) (response WekaStatusResponse, err error) {
	err = c.RunJsonCmd(ctx, []string{
		"wekaauthcli", "status", "--json",
	}, "GetWekaStatus", &response)
	return
}

func (c *CliWekaService) RunJsonCmd(ctx context.Context, cmd []string, name string, data any) error {
	_, _, end := instrumentation.GetLogSpan(ctx, name)
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	output, _, err := executor.ExecNamed(ctx, name, cmd)
	if err != nil {
		return err
	}

	err = json.Unmarshal(output.Bytes(), data)
	if err != nil {
		return err
	}

	return nil
}

func (c *CliWekaService) StartIo(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "StartIo")
	defer end()

	container := c.Container
	executor, err := c.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return &WekaServiceError{
			Err:       err,
			Container: container,
			Message:   "Failed to create executor",
		}
	}

	logger.SetPhase("STARTING_IO")
	cmd := "weka cluster start-io"
	_, stderr, err := executor.ExecNamed(ctx, "StartIO", []string{"bash", "-ce", cmd})
	if err != nil {
		return &WekaCliError{
			WekaServiceError: WekaServiceError{
				Err:       err,
				Container: container,
				Message:   "Failed to start-io",
			},
			Command: cmd,
			Stderr:  stderr.String(),
		}
	}
	logger.InfoWithStatus(codes.Ok, "IO started")
	logger.SetPhase("IO_STARTED")
	return nil
}
