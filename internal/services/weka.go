package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
)

type WekaStatusCapacity struct {
	UnprovisionedBytes int64 `json:"unprovisioned_bytes"`
	TotalBytes         int64 `json:"total_bytes"`
	UnavailableBytes   int64 `json:"unavailable_bytes"`
	HotSpareBytes      int64 `json:"hot_spare_bytes"`
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

type ProtectionState struct {
	MiB         int64   `json:"MiB"`
	NumFailures int     `json:"numFailures"`
	Percent     float64 `json:"percent"`
}

type WekaStatusRebuild struct {
	MovingData      bool    `json:"movingData"`
	ProgressPercent float64 `json:"progressPercent"`
	ProtectionState []ProtectionState
}

func (status *WekaStatusRebuild) IsFullyProtected() bool {
	for _, protectionState := range status.ProtectionState {
		if protectionState.Percent > 0 && protectionState.NumFailures > 0 {
			return false
		}
	}
	return true
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
	HotSpare          int                     `json:"hot_spare"`
	StripeWidth       int                     `json:"stripe_data_drives"`
	RedundancyLevel   int                     `json:"stripe_protection_drives"`
}

// WekaObsBucket represents an Object Store bucket in a Weka filesystem
type WekaObsBucket struct {
	DetachProgress interface{} `json:"detachProgress"`
	DetachTaskId   string      `json:"detachTaskId"`
	Mode           string      `json:"mode"`
	Name           string      `json:"name"`
	ObsBucketId    string      `json:"obsBucketId"`
	State          string      `json:"state"`
	Status         string      `json:"status"`
	Uid            string      `json:"uid"`
}

// WekaFilesystem represents an individual filesystem from the weka fs command
type WekaFilesystem struct {
	AvailableSsd int64           `json:"available_ssd"`
	FsId         string          `json:"fs_id"`
	GroupId      string          `json:"group_id"`
	GroupName    string          `json:"group_name"`
	IsReady      bool            `json:"is_ready"`
	Name         string          `json:"name"`
	ObsBuckets   []WekaObsBucket `json:"obs_buckets"`
	SsdBudget    int64           `json:"ssd_budget"`
	Status       string          `json:"status"`
	TotalBudget  int64           `json:"total_budget"`
	Uid          string          `json:"uid"`
	UsedSsd      int64           `json:"used_ssd"`
	UsedTotal    int64           `json:"used_total"`
}

// WekaCapacityInfo provides information about total provisioned capacity
type WekaCapacityInfo struct {
	// TotalProvisionedCapacity is the sum of total_budget for all filesystems
	TotalProvisionedCapacity int64 `json:"total_provisioned_capacity"`

	// TotalUsedCapacity is the sum of used_total for all filesystems
	TotalUsedCapacity int64 `json:"total_used_capacity"`

	// TotalAvailableCapacity is the difference between TotalProvisionedCapacity and TotalUsedCapacity
	TotalAvailableCapacity int64 `json:"total_available_capacity"`

	// TotalAvailableSSDCapacity is the sum of available_ssd for all filesystems
	TotalAvailableSSDCapacity int64 `json:"total_available_ssd_capacity"`

	// TotalProvisionedSSDCapacity is the sum of ssd_budget for all filesystems
	TotalProvisionedSSDCapacity int64 `json:"total_provisioned_ssd_capacity"`

	// TotalUsedSSDCapacity is the sum of used_ssd for all filesystems
	TotalUsedSSDCapacity int64 `json:"total_used_ssd_capacity"`

	// HasTieredFilesystems indicates if any filesystem is using Object Store capacity
	HasTieredFilesystems bool `json:"has_tiered_filesystems"`

	// TotalObsCapacity is the total Object Store capacity if present
	TotalObsCapacity int64 `json:"total_obs_capacity,omitempty"`

	// Filesystems contains capacity details for each filesystem
	Filesystems []WekaFilesystem `json:"filesystems"`

	// ObsBucketCount is the total number of OBS buckets
	ObsBucketCount int64 `json:"obs_bucket_count"`

	// ActiveObsBucketCount is the number of active OBS buckets
	ActiveObsBucketCount int64 `json:"active_obs_bucket_count"`
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

type S3Cluster struct {
	// indicates if the S3 cluster exists
	Active       bool     `json:"active"`
	S3Hosts      []string `json:"s3_hosts"`
	Port         string   `json:"port"`
	InternalPort string   `json:"internal_port"`
	Filesystem   string   `json:"filesystem_name"`
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

// Examples of internalStatus:
// - when container is part of the cluster:
//
//	"internalStatus": {
//		"action": "NONE",
//		"display_status": "READY",
//		"message": "Ready",
//		"state": "READY"
//	}
//
// - when container cannot join cluster:
//
//	 "internalStatus": {
//		"action": "NONE",
//		"display_status": "CLUSTERIZING",
//		"message": "Joining cluster",
//		"state": "READY"
//	}
//
// - when container is in STEM mode:
//
//	"internalStatus": {
//		"action": "NONE",
//		"display_status": "STEM",
//		"message": "STEM mode",
//		"state": "READY"
//	}
type WekaLocalInternalStatus struct {
	Action        string `json:"action"`
	DisplayStatus string `json:"display_status"`
	Message       string `json:"message"`
	State         string `json:"state"`
}

type WekaLocalContainer struct {
	Name            string                  `json:"name"`
	Type            string                  `json:"type"`
	RunStatus       string                  `json:"runStatus"`
	IsRunning       bool                    `json:"isRunning"`
	LastFailure     string                  `json:"lastFailure"`
	LastFailureTime string                  `json:"lastFailureTime"`
	InternalStatus  WekaLocalInternalStatus `json:"internalStatus"`
}

type WekaClusterContainer struct {
	HostId        string `json:"host_id"`
	HostIp        string `json:"host_ip"`
	Hostname      string `json:"hostname"`
	ContainerName string `json:"container_name"`
	Uid           string `json:"uid"`
	State         string `json:"state"`
	Status        string `json:"status"`
}

type Process struct {
	Status   string   `json:"status"`
	NodeId   string   `json:"node_id"`
	Roles    []string `json:"roles"`
	NodeInfo struct {
		HostId         string   `json:"host_id"`
		ContainerName  string   `json:"container_name"`
		Slot           int      `json:"slot"`
		ManagementIps  []string `json:"mgmt_ips"`
		ManagementPort int      `json:"mgmt_port"`
	}
}

func (p Process) GetProcessId() int {
	id, err := resources.NodeIdToProcessId(p.NodeId)
	if err != nil {
		return -1
	}
	return id
}

type Drive struct {
	Uuid         string `json:"uuid"`
	AddedTime    string `json:"added_time"`
	DevicePath   string `json:"device_path"`
	SerialNumber string `json:"serial_number"`
	Status       string `json:"status"`
}

type DriveListOptions struct {
	ContainerId *int    `json:"container_id"`
	Status      *string `json:"status"`
}

type ProcessListOptions struct {
	ContainerId *int `json:"container_id"`
}

type WekaService interface {
	GetWekaStatus(ctx context.Context) (WekaStatusResponse, error)
	CreateFilesystem(ctx context.Context, name, group string, params FSParams) error
	CreateFilesystemGroup(ctx context.Context, name string) error
	ConfigureNfs(ctx context.Context, nfsParams NFSParams) error
	GetS3Cluster(ctx context.Context) (*S3Cluster, error)
	CreateS3Cluster(ctx context.Context, s3Params S3Params) error
	ListS3ClusterContainers(ctx context.Context) ([]int, error)
	DeleteS3Cluster(ctx context.Context) error
	JoinS3Cluster(ctx context.Context, containerId int) error
	RemoveFromS3Cluster(ctx context.Context, containerId int) error
	JoinNfsInterfaceGroups(ctx context.Context, containerId int) error
	RemoveNfsInterfaceGroups(ctx context.Context, containerId int) error
	GenerateJoinSecret(ctx context.Context) (string, error)
	GetUsers(ctx context.Context) ([]WekaUserResponse, error)
	EnsureUser(ctx context.Context, username, password, role string) error
	EnsureNoUser(ctx context.Context, username string) error
	SetWekaHome(ctx context.Context, WekaHomeConfig v1alpha1.WekaHomeConfig) error
	EmitCustomEvent(ctx context.Context, msg string) error
	ListDrives(ctx context.Context, listOptions DriveListOptions) ([]Drive, error)
	ListContainerDrives(ctx context.Context, containerId int) ([]Drive, error)
	DeactivateContainer(ctx context.Context, containerId int) error
	RemoveDrive(ctx context.Context, driveUuid string) error
	RemoveContainer(ctx context.Context, containerId int) error
	DeactivateDrive(ctx context.Context, driveUuid string) error
	ListProcesses(ctx context.Context, listOptions ProcessListOptions) ([]Process, error)
	ListLocalContainers(ctx context.Context) ([]WekaLocalContainer, error)
	GetWekaContainer(ctx context.Context, containerId int) (*WekaClusterContainer, error)
	GetCapacity(ctx context.Context) (WekaCapacityInfo, error)
	//GetFilesystemByName(ctx context.Context, name string) (WekaFilesystem, error)
}

func NewWekaService(ExecService exec.ExecService, container *v1alpha1.WekaContainer) WekaService {
	return &CliWekaService{
		ExecService: ExecService,
		Container:   container,
	}
}

func NewWekaServiceWithTimeout(ExecService exec.ExecService, container *v1alpha1.WekaContainer, timeout *time.Duration) WekaService {
	return &CliWekaService{
		ExecService: ExecService,
		Container:   container,
		timeout:     timeout,
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
	timeout     *time.Duration
}

func (c *CliWekaService) ListProcesses(ctx context.Context, listOptions ProcessListOptions) ([]Process, error) {
	var processes []Process
	filters := []string{}
	wekacli := "wekaauthcli"
	cmdParts := []string{wekacli, "cluster", "process", "--json"}
	if listOptions.ContainerId != nil {
		filters = append(filters, fmt.Sprintf("containerId=%d", *listOptions.ContainerId))
	}
	if len(filters) != 0 {
		cmdParts = append(cmdParts, "--filter")
		cmdParts = append(cmdParts, strings.Join(filters, ","))
	}

	err := c.RunJsonCmd(ctx, cmdParts, "ListProcesses", &processes)
	if err != nil {
		return nil, err
	}
	return processes, nil
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

func (c *CliWekaService) EmitCustomEvent(ctx context.Context, msg string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EmitCustomEvent")
	defer end()

	logger.Info("Emitting custom event", "msg", msg)

	executor, err := c.GetExecutor(ctx)
	if err != nil {
		logger.SetError(err, "Failed to get executor")
		return err
	}

	cmd := fmt.Sprintf("wekaauthcli events trigger-event \"K8s_Operator: %s\"", msg)
	_, stderr, err := executor.ExecNamed(ctx, "EmitCustomEvent", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.SetError(err, "Failed to emit event", "stderr", stderr.String())
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
		"wekaauthcli", "cluster", "join-token", "generate", "--access-token-timeout", "5200w", "--json",
	}, "GenerateJoinSecret", &data)
	if err != nil {
		return "", err
	}
	return data, nil
}

func (c *CliWekaService) JoinS3Cluster(ctx context.Context, containerId int) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "JoinS3Cluster")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		logger.SetError(err, "Failed to get executor")
		return err
	}

	cmd := []string{
		"wekaauthcli", "s3", "cluster", "containers", "add", strconv.Itoa(containerId),
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "JoinS3Cluster", cmd)
	// error: Trying to set an invalid S3 configuration: Specified host HostId<4> is already part of the S3 cluster
	if err != nil && strings.Contains(stderr.String(), "already part of the S3 cluster") {
		return nil
	}
	if err != nil {
		logger.SetError(err, "Failed to join S3 cluster", "stderr", stderr.String(), "stdout", stdout.String())
		return err
	}

	return nil
}

func (c *CliWekaService) RemoveFromS3Cluster(ctx context.Context, containerId int) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RemoveFromS3Cluster")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	cmd := []string{
		"wekaauthcli", "s3", "cluster", "containers", "remove", strconv.Itoa(containerId),
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "RemoveFromS3Cluster", cmd)
	if err != nil {
		if strings.Contains(stderr.String(), "is not part of the S3 cluster") {
			logger.Warn("Container is not part of the S3 cluster", "containerId", containerId, "err", stderr.String(), "stdout", stdout.String())
			return nil
		}
		if strings.Contains(stderr.String(), fmt.Sprintf("error: Unrecognized host ID HostId<%d>", containerId)) {
			logger.Warn("Container is not recognized by the S3 cluster", "containerId", containerId, "err", stderr.String(), "stdout", stdout.String())
			return nil

		}
		logger.Error(err, "Failed to remove from S3 cluster", "stderr", stderr.String(), "stdout", stdout.String())
		return err
	}

	return nil
}

func (c *CliWekaService) JoinNfsInterfaceGroups(ctx context.Context, containerId int) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "JoinNfsInterfaceGroups")
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
		if len(c.Container.Status.GetManagementIps()) == 0 {
			return errors.New("No management IP addresses found")
		}
		mngmtIps := c.Container.Status.GetManagementIps()
		interfaceName, err = c.GetInterfaceNameByIpAddress(ctx, mngmtIps[0])
		if err != nil {
			logger.SetError(err, "Failed to get interface name by IP address", "ip", mngmtIps[0])
			return err
		}
	}

	// TODO: should we add port for each interface if there are multiple management IPs on container?
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

func (c *CliWekaService) RemoveNfsInterfaceGroups(ctx context.Context, containerId int) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RemoveNfsInterfaceGroups")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	containerIdStr := strconv.Itoa(containerId)
	interfaceName := c.Container.Spec.Network.EthDevice
	interfaceGroupName := "MgmtInterfaceGroup"
	if interfaceName == "" {
		if len(c.Container.Status.GetManagementIps()) == 0 {
			return errors.New("No management IP addresses found")
		}
		mngmtIps := c.Container.Status.GetManagementIps()
		interfaceName, err = c.GetInterfaceNameByIpAddress(ctx, mngmtIps[0])
		if err != nil {
			logger.SetError(err, "Failed to get interface name by IP address", "ip", mngmtIps[0])
			return err
		}
	}

	cmd := []string{
		"wekaauthcli", "nfs", "interface-group", "port", "delete", "-f", interfaceGroupName, containerIdStr, interfaceName,
	}
	_, stderr, err := executor.ExecNamed(ctx, "RemoveNfsInterfaceGroups", cmd)
	if err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "The given interface group name") && strings.Contains(msg, "is unknown") {
			logger.Warn("NFS interface group does not exist", "interfaceGroup", interfaceGroupName)
			return nil
		}
		if strings.Contains(msg, "is not part of group") {
			logger.Warn("Container is not part of the NFS interface group", "containerId", containerId, "stderr", msg)
			return nil
		}
		logger.SetError(err, "Failed to remove NFS interface group", "interfaceGroup", interfaceGroupName, "stderr", msg)
		return err
	}
	return nil
}

func (c *CliWekaService) CreateS3Cluster(ctx context.Context, s3Params S3Params) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CreateS3Cluster")
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

func (c *CliWekaService) ListS3ClusterContainers(ctx context.Context) ([]int, error) {
	// weka s3 cluster containers list --json
	// [
	// 	"HostId<0>",
	// 	"HostId<8>"
	// ]
	var hostIdStrings []string

	cmd := []string{
		"wekaauthcli", "s3", "cluster", "containers", "list", "--json",
	}
	err := c.RunJsonCmd(ctx, cmd, "ListS3ClusterContainers", &hostIdStrings)
	if err != nil {
		err = fmt.Errorf("failed to list S3 cluster containers: %w", err)
		return nil, err
	}

	containerIds := make([]int, 0, len(hostIdStrings))
	for _, hostIdStr := range hostIdStrings {
		id, err := resources.HostIdToContainerId(hostIdStr)
		if err != nil {
			return nil, err
		}
		containerIds = append(containerIds, id)
	}
	return containerIds, nil
}

func (c *CliWekaService) DeleteS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DeleteS3Cluster")
	defer end()

	executor, err := c.ExecService.GetExecutor(ctx, c.Container)
	if err != nil {
		return err
	}

	cmd := []string{
		"wekaauthcli", "s3", "cluster", "destroy", "-f",
	}

	_, stderr, err := executor.ExecNamed(ctx, "DeleteS3Cluster", cmd)
	if err != nil {
		logger.Error(err, "Failed to delete S3 cluster", "stderr", stderr.String())
		return err
	}
	return nil
}

func (c *CliWekaService) ConfigureNfs(ctx context.Context, nfsParams NFSParams) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ConfigureNfs")
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
		logger.SetError(err, "Failed to configure NFS config filesystem", "stderr", stderr.String(), "stdout", stdout.String())
		return err
	}

	if len(nfsParams.SupportedVersions) > 0 {
		cmd = []string{
			"wekaauthcli", "nfs", "global-config", "set", "--supported-versions", strings.Join(nfsParams.SupportedVersions, ","),
		}
		stdout, stderr, err = executor.ExecNamed(ctx, "ConfigureNfsSupportedVersions", cmd)
		if err != nil {
			logger.SetError(err, "Failed to configure NFS supported versions", "stderr", stderr.String(), "stdout", stdout.String())
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
			logger.SetError(err, "Failed to configure NFS interface group", "interfaceGroup", interfaceGroupName, "stderr", stderr.String())
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
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
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
	ctx, _, end := instrumentation.GetLogSpan(ctx, name)
	defer end()

	executor, err := c.ExecService.GetExecutorWithTimeout(ctx, c.Container, c.timeout)
	if err != nil {
		return err
	}

	output, stderr, err := executor.ExecNamed(ctx, name, cmd)
	if err != nil {
		err = fmt.Errorf("failed to run command %s: %v - %s", strings.Join(cmd, " "), err, stderr.String())
		return err
	}

	err = json.Unmarshal(output.Bytes(), data)
	if err != nil {
		err = fmt.Errorf("failed to unmarshal json: %v", err)
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
		if strings.Contains(stderr.String(), fmt.Sprintf("Host HostId<%d> not found", containerId)) {
			return nil
		}
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
	// handle error: The given drive "3b265a18-3e1a-45ab-abe3-a7729497cb1a" does not exist.
	if err != nil && strings.Contains(stderr.String(), "does not exist") {
		return nil
	}
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
		// error: Host HostId<15> not found\n\x00
		if strings.Contains(stderr.String(), "not found") {
			return nil
		}
		err = errors.Wrapf(err, "Failed to remove container %d: %s", containerId, stderr.String())
		return err
	}
	return nil
}

func (c *CliWekaService) ListContainerDrives(ctx context.Context, containerId int) ([]Drive, error) {
	cmd := []string{
		"wekaauthcli", "cluster", "drive", "--container", strconv.Itoa(containerId), "--json",
	}

	drives := []Drive{}
	err := c.RunJsonCmd(ctx, cmd, "ListContainerDrives", &drives)
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

func (c *CliWekaService) ListLocalContainers(ctx context.Context) ([]WekaLocalContainer, error) {
	cmd := []string{
		"weka", "local", "ps", "--json",
	}

	var containers []WekaLocalContainer
	err := c.RunJsonCmd(ctx, cmd, "WekaLocalPs", &containers)
	if err != nil {
		return nil, err
	}
	return containers, nil
}

func (c *CliWekaService) GetS3Cluster(ctx context.Context) (*S3Cluster, error) {
	// weka s3 cluster --json
	cmd := []string{
		"wekaauthcli", "s3", "cluster", "--json",
	}

	var s3Cluster S3Cluster
	err := c.RunJsonCmd(ctx, cmd, "GetS3Cluster", &s3Cluster)
	if err != nil {
		return nil, err
	}
	return &s3Cluster, nil
}

func (c *CliWekaService) GetWekaContainer(ctx context.Context, containerId int) (*WekaClusterContainer, error) {
	cmd := []string{
		"wekaauthcli", "cluster", "container", strconv.Itoa(containerId), "--json",
	}

	var containers []WekaClusterContainer
	err := c.RunJsonCmd(ctx, cmd, "GetWekaContainer", &containers)
	if err != nil {
		return nil, err
	}

	if len(containers) == 0 {
		return nil, fmt.Errorf("container %d not found", containerId)
	}

	return &containers[0], nil
}

// GetCapacity returns information about the total provisioned capacity for all filesystems
func (c *CliWekaService) GetCapacity(ctx context.Context) (WekaCapacityInfo, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetCapacity")
	defer end()

	var filesystems []WekaFilesystem
	err := c.RunJsonCmd(ctx, []string{
		"weka", "fs", "--json",
	}, "GetFilesystems", &filesystems)
	if err != nil {
		logger.SetError(err, "Failed to get filesystems")
		return WekaCapacityInfo{}, err
	}

	capacity := WekaCapacityInfo{
		Filesystems: filesystems,
	}

	// Count OBS buckets
	var obsCount, activeObsCount int64

	// Calculate total capacity information
	for _, fs := range filesystems {
		// Skip filesystems that are not ready
		if !fs.IsReady {
			continue
		}

		// Total capacity calculations
		capacity.TotalProvisionedCapacity += fs.TotalBudget
		capacity.TotalUsedCapacity += fs.UsedTotal

		// SSD capacity calculations
		capacity.TotalAvailableSSDCapacity += fs.AvailableSsd
		capacity.TotalProvisionedSSDCapacity += fs.SsdBudget
		capacity.TotalUsedSSDCapacity += fs.UsedSsd

		// Check if this filesystem has Object Store buckets
		if len(fs.ObsBuckets) > 0 {
			capacity.HasTieredFilesystems = true

			// Count the total number of buckets and active buckets
			obsCount += int64(len(fs.ObsBuckets))

			// Count active buckets (with status "UP")
			for _, bucket := range fs.ObsBuckets {
				if bucket.Status == "UP" {
					activeObsCount++
				}
			}
		}
	}

	// Set OBS metrics
	capacity.ObsBucketCount = obsCount
	capacity.ActiveObsBucketCount = activeObsCount
	capacity.TotalObsCapacity = capacity.TotalProvisionedCapacity - capacity.TotalProvisionedSSDCapacity

	// Calculate available capacity
	capacity.TotalAvailableCapacity = capacity.TotalProvisionedCapacity - capacity.TotalUsedCapacity

	return capacity, nil
}
