package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
)

type WekaStatusCapacity struct {
	UnprovisionedBytes int64 `json:"unprovisioned_bytes"`
	TotalBytes         int64 `json:"total_bytes"`
}

type WekaStatusActivity struct {
	NumOps                    float64 `json:"num_ops"`
	NumReads                  float64 `json:"num_reads"`
	NumWrites                 float64 `json:"num_writes"`
	ObsDownloadBytesPerSecond float64 `json:"obs_download_bytes_per_second"`
	ObsUploadBytesPerSecond   float64 `json:"obs_upload_bytes_per_second"`
	SumBytesRead              float64 `json:"sum_bytes_read"`
	SumBytesWritten           float64 `json:"sum_bytes_written"`
}

type WekaStatusCloud struct {
	Enabled bool   `json:"enabled"`
	Healthy bool   `json:"healthy"`
	Proxy   string `json:"proxy"`
	Url     string `json:"url"`
}

type WekaStatusObjectCounter struct {
	Active int `json:"active"`
	Total  int `json:"total"`
}

type WekaStatusContainers struct {
	WekaStatusObjectCounter
	Backends WekaStatusObjectCounter `json:"backends"`
	Clients  WekaStatusObjectCounter `json:"clients"`
	Computes WekaStatusObjectCounter `json:"computes"`
	Drives   WekaStatusObjectCounter `json:"drives"`
}

type WekaStatusRebuild struct {
	MovingData      bool `json:"movingData"`
	ProgressPercent int  `json:"progressPercent"`
}

type WekaStatusResponse struct {
	ActiveAlertsCount int                     `json:"active_alerts_count"`
	Activity          WekaStatusActivity      `json:"activity"`
	Status            string                  `json:"status"`
	Capacity          WekaStatusCapacity      `json:"capacity"`
	Cloud             WekaStatusCloud         `json:"cloud"`
	Containers        WekaStatusContainers    `json:"containers"`
	Drives            WekaStatusObjectCounter `json:"drives"`
	Rebuild           WekaStatusRebuild       `json:"rebuild"`
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

type NFSParams struct {
	ConfigFilesystem  string
	SupportedVersions []string
}

type WekaUserResponse struct {
	//OrgId    int    `json:"org_id"`
	//PosixGid string `json:"posix_gid"`
	// PosixUid string `json:"posix_uid"`
	// Role     string `json:"role"`
	// S3Policy string `json:"s3_policy"`
	// Source   string `json:"source"`
	// Uid      string `json:"uid"`
	Username string `json:"username"`
}

type Drive struct {
	Uuid       string `json:"uuid"`
	AddedTime  string `json:"added_time"`
	DevicePath string `json:"device_path"`
	Serial     string `json:"serial_number"`
	Status     string `json:"status"`
}

type DriveListOptions struct {
	ContainerId *int    `json:"container_id"`
	Status      *string `json:"status"`
}

type WekaService interface {
	GetWekaStatus(ctx context.Context) (WekaStatusResponse, error)
	CreateFilesystem(ctx context.Context, name, group string, params FSParams) error
	CreateFilesystemGroup(ctx context.Context, name string) error
	ConfigureNfsGateway(ctx context.Context, nfsParams NFSParams) error
	CreateS3Cluster(ctx context.Context, s3Params S3Params) error
	JoinS3Cluster(ctx context.Context, containerId int) error
	JoinNfsInterfaceGroups(ctx context.Context, containerId int) error
	GenerateJoinSecret(ctx context.Context) (string, error)
	GetUsers(ctx context.Context) ([]WekaUserResponse, error)
	EnsureUser(ctx context.Context, username, password, role string) error
	EnsureNoUser(ctx context.Context, username string) error
	SetWekaHome(ctx context.Context, WekaHomeConfig v1alpha1.WekaHomeConfig) error
	ListDrives(ctx context.Context, listOptions DriveListOptions) ([]Drive, error)
	ListContainerDrives(ctx context.Context, containerId int) ([]Drive, error)
	DeactivateContainer(ctx context.Context, containerId int) error
	RemoveDrive(ctx context.Context, driveUuid string) error
	RemoveContainer(ctx context.Context, containerId int) error
	DeactivateDrive(ctx context.Context, driveUuid string) error
	//GetFilesystemByName(ctx context.Context, name string) (WekaFilesystem, error)
}

func NewWekaService(ExecService exec.ExecService, container *v1alpha1.WekaContainer) WekaService {
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

type NfsInterfaceGroupExists struct {
	error
}

type NfsInterfaceGroupAlreadyJoined struct {
	error
}

type CliWekaService struct {
	ExecService exec.ExecService
	Container   *v1alpha1.WekaContainer
}

func (c *CliWekaService) ListDrives(ctx context.Context, listOptions DriveListOptions) ([]Drive, error) {
	var drives []Drive
	filters := []string{}
	wekacli := "wekaauthcli"
	cmdParts := []string{wekacli, "cluster", "drive", "--json"}
	if listOptions.ContainerId != nil {
		filters = append(filters, fmt.Sprintf("host=%d", *listOptions.ContainerId))
	}
	if listOptions.Status != nil {
		filters = append(filters, fmt.Sprintf("status=%s", *listOptions.Status))
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

func (c *CliWekaService) SetWekaHome(ctx context.Context, config v1alpha1.WekaHomeConfig) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SetWekaHome")
	defer end()

	executor, err := c.GetExecutor(ctx)
	if err != nil {
		logger.SetError(err, "Failed to get executor")
		return err
	}
	statsStr := "--cloud-stats off"
	if *config.EnableStats == true {
		statsStr = "--cloud-stats on"
	}

	const enableInsecure = `
if ! wekaauthcli debug override list | grep insecure_weka_cloud_https_call; then
	wekaauthcli debug override add --key insecure_weka_cloud_https_call || wekaauthcli debug override add --key insecure_weka_cloud_https_call --force || true
fi
`

	cmds := []string{}
	if config.AllowInsecure {
		cmds = append(cmds, enableInsecure)
	}

	customCaCertConfig := `
if ! wekaauthcli debug override list | grep weka_cloud_ca_cert_path; then
	weka debug override add --key weka_cloud_ca_cert_path --value \"%s\" || weka debug override add --key weka_cloud_ca_cert_path --value \"%s\" --force || true
fi
`
	if config.CacertSecret != "" {
		path := "/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem"
		customCaCertConfig = fmt.Sprintf(customCaCertConfig, path, path)
		cmds = append(cmds, customCaCertConfig)
	}

	cmd := fmt.Sprintf("wekaauthcli cloud enable --cloud-url %s %s", config.Endpoint, statsStr)
	cmds = append(cmds, cmd)

	fullCmd := strings.Join(cmds, "\n")
	_, stderr, err := executor.ExecNamed(ctx, "SetWekaHome", []string{"bash", "-ce", fullCmd})
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
	cmd := "wekaauthcli user -J"
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

	cmd := fmt.Sprintf("wekaauthcli user add %s %s %s", username, role, password)
	_, stderr, err := executor.ExecSensitive(ctx, "AddClusterUser", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to add user: %s", stderr.String())
	}
	return nil
}

func (c *CliWekaService) GetInterfaceNameByIpAddress(ctx context.Context, ip string) (string, error) {
	if ip == "" {
		return "", errors.New("ip address is empty")
	}
	executor, err := c.GetExecutor(ctx)
	if err != nil {
		return "", err
	}

	cmd := fmt.Sprintf("ip route show | grep -w %s | head -1 | awk '{print $5}'", ip)
	stdout, stderr, err := executor.ExecNamed(ctx, "GetInterfaceNameByIpAddress", []string{"bash", "-ce", cmd})
	if err != nil {
		return "", errors.Wrapf(err, "Failed to get interface name: %s", stderr.String())
	}
	return stdout.String(), nil
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
	// error: Trying to set an invalid S3 configuration: Specified host HostId<4> is already part of the S3 cluster
	if err != nil && strings.Contains(stderr.String(), "already part of the S3 cluster") {
		return nil
	}
	if err != nil {
		logger.SetError(err, "Failed to join S3 cluster", "stderr", stderr.String())
		return err
	}

	return nil
}

func (c *CliWekaService) JoinNfsInterfaceGroups(ctx context.Context, containerId int) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "JoinNfsInterfaceGroups")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		logger.SetError(err, "Failed to get executor")
		return err
	}

	containerIdStr := strconv.Itoa(containerId)
	interfaceName := c.Container.Spec.Network.EthDevice
	interfaceGroupName := "MgmtInterfaceGroup"
	if interfaceName == "" {
		if c.Container.Status.ManagementIP == "" {
			return errors.New("No management IP address found")
		}
		interfaceName, err = c.GetInterfaceNameByIpAddress(ctx, c.Container.Status.ManagementIP)
		if err != nil {
			logger.SetError(err, "Failed to get interface name by IP address", "ip", c.Container.Status.ManagementIP)
			return err
		}
	}

	cmd := []string{
		//weka nfs interface-group port add mgmt 11 ens5
		"wekaauthcli", "nfs", "interface-group", "port", "add", interfaceGroupName, containerIdStr, interfaceName,
	}
	_, stderr, err := executor.ExecNamed(ctx, "JoinNfsInterfaceGroup", cmd)
	if err != nil {

		if strings.Contains(stderr.String(), "is already part of group") {
			return NfsInterfaceGroupAlreadyJoined{err}
		} else {
			logger.SetError(err, "Failed to join NFS interface group", "interfaceGroup", interfaceName, "stderr", stderr.String())
			return err
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
		"wekaauthcli", "s3", "cluster", "create", "default", ".config_fs",
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

func (c *CliWekaService) ConfigureNfsGateway(ctx context.Context, nfsParams NFSParams) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "ConfigureNfsGateway")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	cmd := []string{
		"wekaauthcli", "nfs", "global-config", "set", "--config-fs", nfsParams.ConfigFilesystem,
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "ConfigureNfsConfigFilesystem", cmd)
	if err != nil {
		logger.SetError(err, "Failed to configure NFS gateway config filesystem", "stderr", stderr.String(), "stdout", stdout.String())
		return err
	}

	if len(nfsParams.SupportedVersions) > 0 {
		cmd = []string{
			"wekaauthcli", "nfs", "global-config", "set", "--supported-versions", strings.Join(nfsParams.SupportedVersions, ","),
		}
		stdout, stderr, err = executor.ExecNamed(ctx, "ConfigureNfsSupportedVersions", cmd)
		if err != nil {
			logger.SetError(err, "Failed to configure NFS gateway supported versions", "stderr", stderr.String(), "stdout", stdout.String())
			return err
		}
	}
	// create interface group if it doesn't exist
	interfaceGroupName := "MgmtInterfaceGroup"
	cmd = []string{
		"wekaauthcli", "nfs", "interface-group", "add", interfaceGroupName, "NFS",
	}
	_, stderr, err = executor.ExecNamed(ctx, "ConfigureNfsInterfaceGroup", cmd)
	if err != nil {
		if strings.Contains(stderr.String(), "already exists") {
			return NfsInterfaceGroupExists{err}
		} else {
			logger.SetError(err, "Failed to configure NFS gateway interface group", "interfaceGroup", interfaceGroupName, "stderr", stderr.String())
			return err
		}
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
	_, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}
	cmd := []string{
		"weka", "fs", "group", "create", name,
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CreateFilesystemGroup", cmd)
	if err != nil {
		if strings.Contains(stderr.String(), "already exists") {
			logger.Info("fs group already exists", "name", name)
			return &FilesystemGroupExists{err}
		}
		logger.Error(err, "failed to create fs group", "stderr", stderr.String(), "stdout", stdout.String())
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

func (c *CliWekaService) DeactivateContainer(ctx context.Context, containerId int) error {
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}
	cmd := []string{
		"wekaauthcli", "cluster", "container", "deactivate", strconv.Itoa(containerId),
	}
	_, stderr, err := executor.ExecNamed(ctx, "DeactivateContainer", cmd)
	if err != nil {
		err = errors.Wrapf(err, "Failed to deactivate container %d: %s", containerId, stderr.String())
		return err
	}
	return nil
}

func (c *CliWekaService) RemoveDrive(ctx context.Context, driveUuid string) error {
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}
	cmd := []string{
		"wekaauthcli", "cluster", "drive", "remove", driveUuid, "-f",
	}
	_, stderr, err := executor.ExecNamed(ctx, "RemoveDrive", cmd)
	if err != nil {
		err = errors.Wrapf(err, "Failed to remove drive %s: %s", driveUuid, stderr.String())
		return err
	}
	return nil
}

func (c *CliWekaService) RemoveContainer(ctx context.Context, containerId int) error {
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}
	cmd := []string{
		"wekaauthcli", "cluster", "container", "remove", strconv.Itoa(containerId),
	}
	_, stderr, err := executor.ExecNamed(ctx, "RemoveContainer", cmd)
	if err != nil {
		err = errors.Wrapf(err, "Failed to remove container %d: %s", containerId, stderr.String())
		return err
	}
	return nil
}

func (c *CliWekaService) ListContainerDrives(ctx context.Context, containerId int) ([]Drive, error) {
	cmd := []string{
		"wekaauthcli", "cluster", "drive", "--container", strconv.Itoa(containerId), "--json",
	}

	var drives []Drive
	err := c.RunJsonCmd(ctx, cmd, "ListListContainerDrivesDrives", &drives)
	if err != nil {
		return nil, err
	}
	return drives, nil
}

func (c *CliWekaService) DeactivateDrive(ctx context.Context, driveUuid string) error {
	// weka cluster drive deactivate 44ac08c1-8b5e-4b64-a900-08da0d4dcd35 -f
	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	cmd := []string{
		"wekaauthcli", "cluster", "drive", "deactivate", driveUuid, "-f",
	}
	_, stderr, err := executor.ExecNamed(ctx, "DeactivateDrive", cmd)
	if err != nil {
		err = errors.Wrapf(err, "Failed to deactivate drive %s: %s", driveUuid, stderr.String())
		return err
	}
	return nil
}
