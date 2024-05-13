package services

import (
	"context"
	"encoding/json"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"strconv"
	"strings"
)

type WekaStatusCapacity struct {
	UnprovisionedBytes int64 `json:"unprovisioned_bytes"`
	TotalBytes         int64 `json:"total_bytes"`
}

type WekaStatusResponse struct {
	Status   string             `json:"status"`
	Capacity WekaStatusCapacity `json:"capacity"`
}

type WekaFilesystem struct {
}

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

type WekaService interface {
	GetWekaStatus(ctx context.Context) (WekaStatusResponse, error)
	CreateFilesystem(ctx context.Context, name, group string, params FSParams) error
	CreateFilesystemGroup(ctx context.Context, name string) error
	CreateS3Cluster(ctx context.Context, s3Params S3Params) error
	JoinS3Cluster(ctx context.Context, containerId int) error
	//GetFilesystemByName(ctx context.Context, name string) (WekaFilesystem, error)
}

func NewWekaService(ExecService ExecService, container *v1alpha1.WekaContainer) WekaService {
	return &CliWekaService{
		ExecService: ExecService,
		Container:   container,
	}
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
