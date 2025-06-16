package node_agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/hlog"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"golang.org/x/exp/rand"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	metrics2 "github.com/weka/weka-operator/pkg/metrics"
	"github.com/weka/weka-operator/pkg/util"
)

type NodeAgent struct {
	logger         logr.Logger
	containersData containersData
	token          string
	lastTokenPull  time.Time
}

type ContainerInfo struct {
	labels                 map[string]string
	wekaContainerName      string
	cpuInfo                LocalCpuUtilizationResponse
	cpuInfoLastPoll        time.Time
	containerState         LocalConfigStateResponse
	containerStateLastPull time.Time
	lastRegisterTimestamp  time.Time
	containerName          string
	containerId            string
	mode                   string
	scrapeTargets          []ScrapeTarget
	scrappedData           map[ScrapeTarget][]byte
	statsResponse                StatsResponse
	statsResponseLastPoll        time.Time
	pendingIOsFromProcfs         int
	pendingIOsFromProcfsLastPoll time.Time
}

func (i *ContainerInfo) getMaxCpu() float64 {
	var maxCpu float64
	for _, cpuLoad := range i.cpuInfo.Result {
		if cpuLoad.Value != nil && *cpuLoad.Value > maxCpu {
			maxCpu = *cpuLoad.Value
		}
	}
	return maxCpu
}

type containersData struct {
	lock sync.RWMutex
	data map[string]*ContainerInfo
}

type ScrapeTarget struct {
	Port int    `json:"port"`
	Path string `json:"path"`
	//defaults to localhost if not specified
	Endpoint string `json:"endpoint,omitempty"`
	AppName  string `json:"app_name"`
}

type RegisterContainerPayload struct {
	ContainerName     string            `json:"container_name"`
	ContainerId       string            `json:"container_id"`
	WekaContainerName string            `json:"weka_container_name"`
	Labels            map[string]string `json:"labels"`
	Mode              string            `json:"mode"`
	ScrapeTargets     []ScrapeTarget    `json:"scrape_targets"`
}

type ProcessSummary struct {
	Up    int `json:"up"`
	Total int `json:"total"`
}

type WekaDrive struct {
	DiskId       string `json:"disk_id"` // "DiskId<1>",
	Uid          string `json:"uid"`
	SerialNumber string `json:"serial_number"`
	DevUuid      string `json:"dev_uuid"`
	Status       string `json:"status"`
	IsFailed     bool   `json:"isFailed"`
	Name         string `json:"name"`
}

type LocalConfigStateResponse struct {
	Result struct {
		HasLease           *bool `json:"has_lease"`
		FilesystemsSummary []struct {
			FilesystemId string `json:"filesystem_id"` // FSId<0>
			Name         string `json:"name"`          // .config_fs
		} `json:"filesystems_summary"`
		DisksSummary     []WekaDrive `json:"disks_summary"`
		HostName         string      `json:"host_name"`
		HostId           string      `json:"host_id"`
		ProcessesSummary struct {
			Drive      ProcessSummary `json:"drive"`
			Dataserv   ProcessSummary `json:"dataserv"`
			Management ProcessSummary `json:"management"`
			Compute    ProcessSummary `json:"compute"`
			Total      ProcessSummary `json:"total"`
			Frontend   ProcessSummary `json:"frontend"`
		} `json:"processes_summary"`
	} `json:"result"`
}

func NewNodeAgent(logger logr.Logger) *NodeAgent {
	rand.Seed(uint64(time.Now().UnixNano()))
	return &NodeAgent{
		logger: logger,
		containersData: containersData{
			lock: sync.RWMutex{},
			data: make(map[string]*ContainerInfo),
		},
	}
}

// getPendingIOsFromProcfs reads the /proc/wekafs/<wekaContainerName>/queue file
// and counts IO operations with an age greater than 1 minute.
// The wekaContainerName is expected to be the directory name in /proc/wekafs/
func getPendingIOsFromProcfs(ctx context.Context, wekaContainerName string) (int, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "getPendingIOsFromProcfs", "wekaContainerName", wekaContainerName)
	defer end()

	filePath := fmt.Sprintf("/proc/wekafs/%s/queue", wekaContainerName)
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.V(1).Info("Procfs queue file not found, assuming 0 pending IOs.", "path", filePath)
			return 0, nil // File not existing could mean client is not active or no IOs, treat as 0.
		}
		return 0, errors.Wrapf(err, "failed to open procfs queue file %s", filePath)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	pendingIOsCount := 0
	inDataSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if !inDataSection {
			// Header line starts with '#' and contains specific keywords
			if strings.HasPrefix(line, "#") && strings.Contains(line, "FE") && strings.Contains(line, "opcode") && strings.Contains(line, "age") {
				inDataSection = true
			}
			continue // Skip lines until the data section header is found
		}

		// Lines indicating end of data section or summary lines
		if line == "" || strings.HasPrefix(line, "total:") || strings.HasPrefix(line, "Gateway is aware") || strings.HasPrefix(line, "Container ") {
			break // Stop parsing if we are past the IO data lines
		}

		fields := strings.Fields(line)
		// Age and unit are expected to be the last two fields.
		if len(fields) < 2 {
			logger.V(2).Info("Skipping short/malformed line in procfs queue file", "line", line)
			continue
		}

		ageStr := fields[len(fields)-2]
		unitStr := fields[len(fields)-1]

		if unitStr != "ms" {
			// This might be a line that is not an IO entry, or a malformed entry.
			logger.V(2).Info("Unexpected age unit or not an IO data line in procfs queue file", "unit", unitStr, "line", line)
			continue
		}

		ageMs, errConv := strconv.ParseInt(ageStr, 10, 64)
		if errConv != nil {
			logger.V(1).Info("Failed to parse age in procfs queue file", "age", ageStr, "line", line, "error", errConv)
			continue
		}

		if ageMs > 60000 { // 1 minute = 60,000 milliseconds
			pendingIOsCount++
		}
	}

	if errScan := scanner.Err(); errScan != nil {
		return 0, errors.Wrapf(errScan, "error scanning procfs queue file %s", filePath)
	}

	logger.V(1).Info("Successfully parsed procfs queue file", "path", filePath, "pendingIOsCount", pendingIOsCount)
	return pendingIOsCount, nil
}

func (a *NodeAgent) PanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				e := fmt.Errorf("panic: %v", err)
				a.logger.Error(e, string(debug.Stack()))
			}
		}()
		next.ServeHTTP(w, req)
	})
}

func (a *NodeAgent) LoggingMiddleware(next http.Handler) http.Handler {
	return hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
		name := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		_, logger := instrumentation.GetLoggerForContext(r.Context(), &a.logger, name)

		logger.V(0).Info("", "status", status, "size", size, "duration", duration)
	})(next)
}

func (a *NodeAgent) ConfigureHttpServer(ctx context.Context) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", a.metricsHandler)
	mux.HandleFunc("/getContainerInfo", a.getContainerInfo)
	mux.HandleFunc("/getActiveMounts", a.getActiveMounts)
	mux.HandleFunc("/register", a.registerHandler)

	// Use custom middleware to log requests and recover from panics
	wrappedMux := a.PanicRecovery(a.LoggingMiddleware(mux))

	bindTo := config.Config.BindAddress.NodeAgent
	a.logger.Info("Server is binding to " + bindTo)

	httpServer := &http.Server{
		Addr:    bindTo,
		Handler: wrappedMux,
	}
	return httpServer, nil
}

func (a *NodeAgent) Run(ctx context.Context, server *http.Server) error {
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (a *NodeAgent) metricsHandler(writer http.ResponseWriter, request *http.Request) {
	ctx, logger, end := instrumentation.GetLogSpan(request.Context(), "Metrics")
	defer end()
	a.containersData.lock.RLock()
	wg := sync.WaitGroup{}
	wg.Add(len(a.containersData.data))

	//TODO: Throttle actual fetch? it should be very lightweight, just to prevent DoS(Accidental including) against management
	for _, container := range a.containersData.data {
		go func(container *ContainerInfo) {
			defer wg.Done()
			deadlineCtx, cancel := context.WithDeadline(ctx, time.Now().Add(5*time.Second))
			defer cancel()
			err := a.fetchAndPopulateMetrics(deadlineCtx, container)
			if err != nil {
				logger.Error(err, "Failed to fetch and populate metrics", "container_name", container.containerName)
			}
		}(container)
	}
	a.containersData.lock.RUnlock() // Don't forget to unlock
	wg.Wait()

	promResponse := metrics2.NewPromResponse()

	a.containersData.lock.RLock()
	defer a.containersData.lock.RUnlock()

	for _, container := range a.containersData.data {
		defaultLabels := make(map[string]string)
		for key, value := range container.labels {
			label := metrics2.NormalizeLabelName(key)
			defaultLabels[label] = value
		}

		// Add node_name to all metrics
		if config.Config.MetricsServerEnv.NodeName != "" {
			defaultLabels["node_name"] = config.Config.MetricsServerEnv.NodeName
		}

		containerLabels := util.MergeMaps(defaultLabels,
			metrics2.TagMap{
				"container_name": container.containerName,
				"mode":           container.mode,
			})
		if container.mode == weka.WekaContainerModeClient {
			//containerLabels[domain] = container.containerName
			containerLabels["weka_io_cluster_name"] = defaultLabels["weka_io_target_cluster_name"]
		}

		if cluster_name, ok := defaultLabels["weka_io_cluster_name"]; ok {
			containerLabels["cluster_name"] = cluster_name
		}

		for _, target := range container.scrapeTargets {
			if container.scrappedData == nil {
				continue
			}
			data, ok := container.scrappedData[target]
			if !ok {
				continue
			}

			data, err := TransformMetrics(data, containerLabels, target.AppName+"_")
			if err != nil {
				logger.Error(err, "Failed to transform metrics", "container_name", container.containerName)
				continue
			}
			promResponse.AddBytes(data)
		}

		switch container.mode {
		case weka.WekaContainerModeEnvoy: // nothing todo, scrapeTargets is common
		default:
			if container.containerStateLastPull.Add(5 * time.Minute).Before(time.Now()) {
				continue
			}
			promResponse.AddMetric(metrics2.PromMetric{
				Metric: "weka_processes_count",
				Help:   "Weka processes counter by state up/down",
			},
				[]metrics2.TaggedValue{
					{
						Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"status": "up"}),
						Value:     float64(container.containerState.Result.ProcessesSummary.Total.Up),
						Timestamp: container.containerStateLastPull,
					},
					{
						Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"status": "down"}),
						Value:     float64(container.containerState.Result.ProcessesSummary.Total.Total - container.containerState.Result.ProcessesSummary.Total.Up),
						Timestamp: container.containerStateLastPull,
					},
				},
			)

			for nodeIdStr, cpuLoad := range container.cpuInfo.Result {
				// do we care about the node id? should we report per node or containers totals? will stay with totals for now

				processId, err := resources.NodeIdToProcessId(nodeIdStr)
				if err != nil {
					logger.Error(err, "Failed to convert node id to process id", "node_id", nodeIdStr)
					continue
				}
				processIdStr := fmt.Sprintf("%d", processId)

				value := 0.0
				if cpuLoad.Err != nil && *cpuLoad.Err != "" {
					logger.Error(errors.New(*cpuLoad.Err), "Failed to fetch cpu utilization", "node_id", nodeIdStr)
					continue
				} else {
					if cpuLoad.Value != nil {
						value = *cpuLoad.Value
					}
				}

				promResponse.AddMetric(metrics2.PromMetric{
					Metric: "weka_cpu_utilization_percent",
					Help:   "Weka container CPU utilization",
				}, []metrics2.TaggedValue{
					{
						Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"process_id": processIdStr}),
						Value:     value,
						Timestamp: container.cpuInfoLastPoll,
					},
				})
			}

			for _, disk := range container.containerState.Result.DisksSummary {
				if !slices.Contains([]string{"ACTIVE", "PHASING_IN"}, disk.Status) || disk.IsFailed {
					promResponse.AddMetric(metrics2.PromMetric{
						Metric: "weka_inactive_drives",
						Help:   "Weka processes",
					},
						[]metrics2.TaggedValue{
							{
								Tags: util.MergeMaps(containerLabels, metrics2.TagMap{
									"status":    disk.Status,
									"serial":    disk.SerialNumber,
									"weka_uid":  disk.Uid,
									"is_failed": strconv.FormatBool(disk.IsFailed),
								}),
								Value:     1,
								Timestamp: container.containerStateLastPull,
							},
						},
					)
				}
			}
		}
		// Process local stats-based metrics
		a.addLocalNodeStats(ctx, promResponse, container, containerLabels)
	}

	// Write the response
	writer.Header().Set("Content-Type", "text/plain")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(promResponse.String()))
}

func (a *NodeAgent) getCurrentToken() string {
	// if token is older than 1 minutes, read again
	if a.lastTokenPull.Add(1 * time.Minute).Before(time.Now()) {
		token, err := os.ReadFile("/var/run/secrets/kubernetes.io/token/token")
		if err != nil {
			a.logger.Error(err, "Failed to read token")
			os.Exit(1)
		}
		a.token = string(token)
		a.lastTokenPull = time.Now()
	}
	return a.token
}

func (a *NodeAgent) registerHandler(w http.ResponseWriter, r *http.Request) {
	//TODO: Add secret-based token auth, i.e secret created by operator and shared with node agent
	_, logger, end := instrumentation.GetLogSpan(r.Context(), "RegisterHandler")
	defer end()

	if a.validateAuth(w, r, logger) {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload RegisterContainerPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	a.containersData.lock.Lock()
	defer a.containersData.lock.Unlock()

	a.containersData.data[payload.ContainerId] = &ContainerInfo{
		labels:            payload.Labels,
		wekaContainerName: payload.WekaContainerName,
		containerName:     payload.ContainerName,
		containerId:       payload.ContainerId,
		mode:              payload.Mode,
		//scrapeTargets:     payload.ScrapeTargets,
	}

	logger.Info("Container registered", "container_name", payload.ContainerName, "container_id", payload.ContainerId)

	response := map[string]string{"message": "Container registered successfully"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (a *NodeAgent) validateAuth(w http.ResponseWriter, r *http.Request, logger *instrumentation.SpanLogger) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader != fmt.Sprintf("Token %s", a.getCurrentToken()) {
		logger.Errorf("Unauthorized request")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return true
	}
	return false
}

func jrpcCall(ctx context.Context, container *ContainerInfo, method string, data interface{}) error {
	// TODO: Wrap as a service struct initialized separately from use

	socketPath := fmt.Sprintf("/host-binds/shared/containers/%s/local-sockets/%s/container.sock", container.containerId, container.wekaContainerName)
	// check if socket exists
	if !util.FileExists(socketPath) {
		return errors.New("socket not found")
	}

	// create symlink for a socket within tmp
	targetSocketPath := fmt.Sprintf("/tmp/%s.sock", container.containerId)
	if !util.FileExists(targetSocketPath) {
		err := os.Symlink(socketPath, targetSocketPath)
		if err != nil {
			return err
		}
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", targetSocketPath)
			},
		},
	}

	payload := map[string]interface{}{
		"id":      rand.Uint64(),
		"jsonrpc": "2.0",
		"method":  method,
		"params":  map[string]interface{}{},
	}

	// Marshal payload to JSON
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/api/v1", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("status code: " + resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(data); err != nil {
		return err
	}

	return nil
}

type LocalCpuUtilizationResponse struct {
	Result map[string]struct {
		Value *float64 `json:"value"`
		Err   *string  `json:"err"`
	} `json:"result"`
}

type WekaStat struct {
	Params struct {
		FsId  string `json:"fS,omitempty"`   // integer in string format
		Disk  string `json:"disk,omitempty"` // integer in string format
		FeIdx string `json:"feIdx,omitempty"`
	} `json:"params"`
	Stat     string `json:"stat"`
	NodeId   string `json:"nodeId"` //NodeId<25011>
	Category string `json:"category"`
	Value    string `json:"value"`
	Unit     string `json:"unit"`
}

type StatsResponse struct {
	Result []WekaStat `json:"result"`
}

func (a *NodeAgent) fetchAndPopulateMetrics(ctx context.Context, container *ContainerInfo) error {
	// WARNING: no lock here, while calling in parallel from multiple places

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "fetchAndPopulateMetrics")
	defer end()

	if container.mode != weka.WekaContainerModeEnvoy {
		var response LocalConfigStateResponse
		err := jrpcCall(ctx, container, "get_local_config_summary", &response)
		if err != nil {
			logger.Error(err, "Failed to fetch local config summary, proceeding with other metrics")
			// Do not return; attempt to gather other metrics.
		} else if response.Result.HasLease == nil || *response.Result.HasLease {
			//if no lease info = old version, if has lease field and do not have lease = stale data which we have no interest in
			container.containerState = response
			container.containerStateLastPull = time.Now()
		}

		var cpuResponse LocalCpuUtilizationResponse
		err = jrpcCall(ctx, container, "fetch_local_realtime_cpu_usage", &cpuResponse)
		if err != nil {
			logger.Error(err, "Failed to fetch local realtime cpu usage, proceeding with other metrics")
			// Do not return; attempt to gather other metrics.
		} else {
			container.cpuInfo = cpuResponse
			container.cpuInfoLastPoll = time.Now()
		}

		var statsResponse StatsResponse
		err = jrpcCall(ctx, container, "fetch_local_stats", &statsResponse)
		if err != nil {
			logger.Error(err, "Failed to fetch stats")
		} else {
			container.statsResponse = statsResponse
			container.statsResponseLastPoll = time.Now()
		}

		if container.mode == weka.WekaContainerModeClient {
			pendingIOs, errGetIOs := getPendingIOsFromProcfs(ctx, container.wekaContainerName)
			if errGetIOs != nil {
				// Log error but don't fail the whole metric population for this.
				// The metric will either be absent or stale if this fails.
				logger.Error(errGetIOs, "Failed to get pending IOs from procfs", "wekaContainerName", container.wekaContainerName)
			} else {
				container.pendingIOsFromProcfs = pendingIOs
				container.pendingIOsFromProcfsLastPoll = time.Now()
			}
		}
	}

	for _, target := range container.scrapeTargets {
		// regular http call to localhost:port/path
		// parse response and add to container.scrappedData
		endpoint := target.Endpoint
		if endpoint == "" {
			endpoint = "localhost"
		}
		resp, err := http.Get(fmt.Sprintf("http://%s:%d%s", endpoint, target.Port, target.Path))
		if err != nil {
			logger.Error(err, "Failed to fetch metrics", "target", target)
			continue
		}
		defer resp.Body.Close()
		if container.scrappedData == nil {
			container.scrappedData = make(map[ScrapeTarget][]byte)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Error(err, "Failed to read response body", "target", target)
			continue
		}
		container.scrappedData[target] = data
	}

	return nil
}

type ContainerInfoResponse struct {
	ContainerMetrics weka.WekaContainerMetrics `json:"container_metrics,omitempty"`
}

type GetContainerInfoRequest struct {
	ContainerId string `json:"container_id"`
}

func (a *NodeAgent) getContainerInfo(w http.ResponseWriter, r *http.Request) {
	ctx, logger, end := instrumentation.GetLogSpan(r.Context(), "getContainerInfo")
	defer end()

	if a.validateAuth(w, r, logger) {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed, current method: "+r.Method, http.StatusMethodNotAllowed)
		return
	}

	// polls and returns information about specific container
	// get container id from request body
	var containerInfoRequest GetContainerInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&containerInfoRequest); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	a.containersData.lock.RLock() // TODO: Replace with syncmap
	container, ok := a.containersData.data[containerInfoRequest.ContainerId]
	a.containersData.lock.RUnlock()
	if !ok {
		http.Error(w, "container not found", http.StatusNotFound)
		return
	}

	// fetch container info
	err := a.fetchAndPopulateMetrics(ctx, container)
	if err != nil && strings.Contains(err.Error(), "socket not found") {
		http.Error(w, "container socket not found", http.StatusNotFound)
		return
	}
	if err != nil {
		logger.SetError(err, "Failed to fetch container info")
		http.Error(w, "failed to fetch container info", http.StatusInternalServerError)
		return
	}

	// prepare response
	response := ContainerInfoResponse{
		ContainerMetrics: weka.WekaContainerMetrics{},
	}
	// populate metrics
	response.ContainerMetrics.Processes = weka.EntityStatefulNum{
		Active:  weka.IntMetric(int64(container.containerState.Result.ProcessesSummary.Total.Up)),
		Created: weka.IntMetric(int64(container.containerState.Result.ProcessesSummary.Total.Total)),
	}

	cpuUsage := weka.FloatMetric("")
	response.ContainerMetrics.CpuUsage = cpuUsage
	response.ContainerMetrics.CpuUsage.SetValue(container.getMaxCpu())

	totalDrives := 0
	activeDrives := 0
	failedDrives := []weka.DriveFailures{}

	//TODO: Expand prom metrics with failed disks metrics
	for _, disk := range container.containerState.Result.DisksSummary {
		totalDrives++
		if disk.Status == "ACTIVE" {
			activeDrives++
		} else {
			failedDrives = append(failedDrives, weka.DriveFailures{
				SerialId:    disk.SerialNumber,
				WekaDriveId: disk.Uid,
			})
		}
	}

	if container.mode == weka.WekaContainerModeDrive {
		response.ContainerMetrics.Drives = weka.DriveMetrics{
			DriveCounters: weka.EntityStatefulNum{
				Active:  weka.IntMetric(int64(activeDrives)),
				Created: weka.IntMetric(int64(totalDrives)),
			},
			DriveFailures: failedDrives,
		}
	}

	// write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (a *NodeAgent) getActiveMounts(w http.ResponseWriter, r *http.Request) {
	_, logger, end := instrumentation.GetLogSpan(r.Context(), "GetActiveMounts")
	defer end()

	if a.validateAuth(w, r, logger) {
		return
	}

	// path to driver interface file
	filePath := "/proc/wekafs/interface"

	file, err := os.Open(filePath)
	if err != nil && os.IsNotExist(err) {
		// file does not exist, respond with not found
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		err = fmt.Errorf("failed to open file: %w", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var activeMounts int

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Active mounts:") {
			_, err := fmt.Sscanf(line, "Active mounts: %d", &activeMounts)
			if err != nil {
				err = fmt.Errorf("failed to parse active mounts: %w", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		err = fmt.Errorf("failed to scan file: %w", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]int{"active_mounts": activeMounts})
}

type CategoryStat struct {
	Stat     string `json:"stat"`
	Category string `json:"category"`
}
type GrouppedMetrics map[CategoryStat][]WekaStat

type processedStat struct {
	OriginalStat WekaStat
	Value        float64
	NodeId       int
	FsName       string
	DriveDetails WekaDrive
}

func processStat(ctx context.Context, stat WekaStat, container *ContainerInfo) processedStat {
	_, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	floatVal, err := strconv.ParseFloat(stat.Value, 64)
	if err != nil {
		logger.Info("Failed to parse float", "value", stat.Value)
	}

	nodeIdInt, err := resources.NodeIdToProcessId(stat.NodeId)
	if err != nil {
		logger.Info("Failed to parse node id", "nodeId", stat.NodeId)
	}

	fsName := ""
	if stat.Params.FsId != "" {
		fsName = deduceFsName(ctx, container, stat.Params.FsId)
		if fsName == "" {
			logger.Info("Failed to determine fs name")
		}
	}

	var wekaDrive WekaDrive
	if stat.Params.Disk != "" {
		for _, disk := range container.containerState.Result.DisksSummary {
			configDriveIdInt, err := resources.DriveIdToInteger(disk.DiskId)
			if err != nil {
				logger.Error(err, "Failed to parse disk id", "serialNumber", disk.SerialNumber)
				continue
			}
			paramDriveInt, err := strconv.Atoi(stat.Params.Disk)
			if err != nil {
				logger.Error(err, "Failed to parse disk id", "diskId", stat.Params.Disk)
				continue
			}

			if configDriveIdInt == paramDriveInt {
				wekaDrive = disk
				break
			}
		}
	}

	return processedStat{
		OriginalStat: stat,
		Value:        floatVal,
		NodeId:       nodeIdInt,
		FsName:       fsName,
		DriveDetails: wekaDrive,
	}
}

type MetricDefinition struct {
	PromMetricName string
	Help           string
	Type           string
	Modes          []string
	CategoryStats  []CategoryStat
	ValueTransform func(float64) float64
	TagsFunc       func(p processedStat) metrics2.TagMap
	// CustomGetValueAndTagsFunc provides an alternative way to fetch metric values and tags,
	// bypassing the CategoryStats logic. If set, it's used; otherwise, CategoryStats processing is attempted.
	CustomGetValueAndTagsFunc func(ctx context.Context, container *ContainerInfo, baseLabels metrics2.TagMap) []metrics2.TaggedValue
	Aggregate                 bool
	Reduce                    bool
	ReduceBy                  string
	ReduceKeep                []string
}

func aggregateTaggedValues(values []metrics2.TaggedValue) []metrics2.TaggedValue {
	aggregated := make(map[string]metrics2.TaggedValue)

	for _, tv := range values {
		var sb strings.Builder
		for k, v := range util.MapOrdered(tv.Tags) {
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(v)
			sb.WriteString(",")
		}
		key := sb.String()

		if existing, ok := aggregated[key]; ok {
			existing.Value += tv.Value
			aggregated[key] = existing
		} else {
			aggregated[key] = tv
		}
	}

	finalValues := make([]metrics2.TaggedValue, 0, len(aggregated))
	for _, v := range aggregated {
		finalValues = append(finalValues, v)
	}
	return finalValues
}

func (a *NodeAgent) addLocalNodeStats(ctx context.Context, response *metrics2.PromResponse, container *ContainerInfo, labels map[string]string) {
	_, _, end := instrumentation.GetLogSpan(ctx, "addLocalNodeStats")
	defer end()

	if time.Since(container.containerStateLastPull) > 5*time.Minute {
		return // ignoring all data
	}
	if time.Since(container.statsResponseLastPoll) > 5*time.Minute {
		return //ignoring all data
	}

	//group metrics
	groupedMetrics := make(GrouppedMetrics)
	for _, stat := range container.statsResponse.Result {
		groupedMetrics[CategoryStat{Stat: stat.Stat, Category: stat.Category}] = append(groupedMetrics[CategoryStat{Stat: stat.Stat, Category: stat.Category}], stat)
	}

	latencyToSeconds := func(v float64) float64 { return v / (1000 * 1000) }
	blocksToBytes := func(v float64) float64 { return v * 4096 }

	fsTags := func(p processedStat) metrics2.TagMap {
		return metrics2.TagMap{
			"fs_id":      p.OriginalStat.Params.FsId,
			"fs_name":    p.FsName,
			"process_id": strconv.Itoa(p.NodeId),
		}
	}
	driveTags := func(p processedStat) metrics2.TagMap {
		return metrics2.TagMap{
			"status":     p.DriveDetails.Status,
			"serial":     p.DriveDetails.SerialNumber,
			"weka_uid":   p.DriveDetails.Uid,
			"is_failed":  strconv.FormatBool(p.DriveDetails.IsFailed),
			"process_id": strconv.Itoa(p.NodeId),
		}
	}
	networkTags := func(p processedStat) metrics2.TagMap {
		return metrics2.TagMap{
			"process_id": strconv.Itoa(p.NodeId),
		}
	}

	metricDefinitions := []MetricDefinition{
		{
			PromMetricName: "weka_api_requests_total",
			Help:           "Total API requests by HTTP code group",
			Type:           "counter",
			Modes:          []string{weka.WekaContainerModeCompute, weka.WekaContainerModeDrive},
			CategoryStats: []CategoryStat{
				{Stat: "TOTAL_2xx_RQ", Category: "api"}, {Stat: "TOTAL_3xx_RQ", Category: "api"},
				{Stat: "TOTAL_4xx_RQ", Category: "api"}, {Stat: "TOTAL_429_RQ", Category: "api"},
				{Stat: "TOTAL_5xx_RQ", Category: "api"},
			},
			TagsFunc: func(p processedStat) metrics2.TagMap {
				httpCodeGroup := ""
				switch p.OriginalStat.Stat {
				case "TOTAL_2xx_RQ":
					httpCodeGroup = "2xx"
				case "TOTAL_3xx_RQ":
					httpCodeGroup = "3xx"
				case "TOTAL_4xx_RQ":
					httpCodeGroup = "4xx"
				case "TOTAL_429_RQ":
					httpCodeGroup = "429"
				case "TOTAL_5xx_RQ":
					httpCodeGroup = "5xx"
				}
				return metrics2.TagMap{"http_code_group": httpCodeGroup, "process_id": strconv.Itoa(p.NodeId)}
			},
			Aggregate: true,
		},
		{
			PromMetricName: "weka_fs_read_requests_total",
			Help:           "Number of reads per weka filesystem",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "READS", Category: "fs_stats"}},
			TagsFunc:       fsTags,
			Reduce:         true,
			ReduceBy:       "process_id",
			ReduceKeep:     []string{"fs_id", "fs_name"},
		},
		{
			PromMetricName: "weka_fs_write_requests_total",
			Help:           "Number of writes per weka filesystem",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "WRITES", Category: "fs_stats"}},
			TagsFunc:       fsTags,
			Reduce:         true,
			ReduceBy:       "process_id",
			ReduceKeep:     []string{"fs_id", "fs_name"},
		},
		{
			PromMetricName: "weka_fs_read_seconds_total",
			Help:           "Total read latency per weka filesystem, divide by reads to get average",
			CategoryStats:  []CategoryStat{{Stat: "READ_LATENCY", Category: "fs_stats"}},
			TagsFunc:       fsTags,
			ValueTransform: latencyToSeconds,
			Reduce:         true,
			ReduceBy:       "process_id",
			ReduceKeep:     []string{"fs_id", "fs_name"},
		},
		{
			PromMetricName: "weka_fs_write_seconds_total",
			Help:           "Total write latency per weka filesystem, divide by writes to get average",
			CategoryStats:  []CategoryStat{{Stat: "WRITE_LATENCY", Category: "fs_stats"}},
			TagsFunc:       fsTags,
			ValueTransform: latencyToSeconds,
			Reduce:         true,
			ReduceBy:       "process_id",
			ReduceKeep:     []string{"fs_id", "fs_name"},
		},
		{
			PromMetricName: "weka_fs_read_bytes_total",
			Help:           "Total read bytes per weka filesystem",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "READ_BYTES", Category: "fs_stats"}},
			TagsFunc:       fsTags,
			Reduce:         true,
			ReduceBy:       "process_id",
			ReduceKeep:     []string{"fs_id", "fs_name"},
		},
		{
			PromMetricName: "weka_fs_write_bytes_total",
			Help:           "Total write bytes per weka filesystem",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "WRITE_BYTES", Category: "fs_stats"}},
			TagsFunc:       fsTags,
			Reduce:         true,
			ReduceBy:       "process_id",
			ReduceKeep:     []string{"fs_id", "fs_name"},
		},
		{
			PromMetricName: "weka_client_pending_ios_count",
			Help:           "Total pending IO operations (age > 1min) for a weka client container, from /proc/wekafs.",
			Type:           "gauge",
			Modes:          []string{weka.WekaContainerModeClient},
			// CategoryStats is nil/empty for this custom metric
			TagsFunc: func(p processedStat) metrics2.TagMap { return metrics2.TagMap{} }, // Returns empty, base labels are sufficient
			CustomGetValueAndTagsFunc: func(ctx context.Context, container *ContainerInfo, baseLabels metrics2.TagMap) []metrics2.TaggedValue {
				var taggedValues []metrics2.TaggedValue
				if time.Since(container.pendingIOsFromProcfsLastPoll) <= 5*time.Minute {
					taggedValues = append(taggedValues, metrics2.TaggedValue{
						Tags:      baseLabels, // Custom logic might add more tags here if needed by merging with baseLabels
						Value:     float64(container.pendingIOsFromProcfs),
						Timestamp: container.pendingIOsFromProcfsLastPoll,
					})
				}
				return taggedValues
			},
			// Aggregate: true, // Not strictly needed for a single value metric with simple tags
		},
		{
			PromMetricName: "weka_port_tx_bytes_total",
			Help:           "Total bytes transmitted per weka node",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "PORT_TX_BYTES", Category: "network"}},
			TagsFunc:       networkTags,
		},
		{
			PromMetricName: "weka_port_rx_bytes_total",
			Help:           "Total bytes received per weka node",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "PORT_RX_BYTES", Category: "network"}},
			TagsFunc:       networkTags,
		},
		{
			PromMetricName: "weka_drive_read_requests_total",
			Help:           "Total read operations per weka drive",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "DRIVE_READ_OPS", Category: "ssd"}},
			TagsFunc:       driveTags,
		},
		{
			PromMetricName: "weka_drive_write_requests_total",
			Help:           "Total write operations per weka drive",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "DRIVE_WRITE_OPS", Category: "ssd"}},
			TagsFunc:       driveTags,
		},
		{
			PromMetricName: "weka_drive_write_seconds_total",
			Help:           "Total duration of drive writes per drive drive, divide by writes to get average latency",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "DRIVE_WRITE_LATENCY", Category: "ssd"}},
			TagsFunc:       driveTags,
			ValueTransform: latencyToSeconds,
		},
		{
			PromMetricName: "weka_drive_read_seconds_total",
			Help:           "Total duration of drive reads per drive drive, divide by reads to get average latency",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "DRIVE_READ_LATENCY", Category: "ssd"}},
			TagsFunc:       driveTags,
			ValueTransform: latencyToSeconds,
		},
		{
			PromMetricName: "weka_drive_read_bytes_total",
			Help:           "Total bytes read per drive",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "DRIVE_MEDIA_BLOCKS_READ", Category: "ssd"}},
			TagsFunc:       driveTags,
			ValueTransform: blocksToBytes,
		},
		{
			PromMetricName: "weka_drive_write_bytes_total",
			Help:           "Total bytes written per drive",
			Type:           "counter",
			CategoryStats:  []CategoryStat{{Stat: "DRIVE_MEDIA_BLOCKS_WRITE", Category: "ssd"}},
			TagsFunc:       driveTags,
			ValueTransform: blocksToBytes,
		},
		{
			PromMetricName: "weka_drive_smart_media_errors_sample",
			Help:           "Total media errors per drive",
			Type:           "gauge",
			CategoryStats:  []CategoryStat{{Stat: "NVME_SMART_MEDIA_ERRORS", Category: "ssd"}},
			TagsFunc:       driveTags,
		},
		{
			PromMetricName: "weka_drive_smart_critical_warnings_sample",
			Help:           "Total critical warnings per drive",
			Type:           "gauge",
			CategoryStats:  []CategoryStat{{Stat: "NVME_SMART_CRITICAL_WARNING", Category: "ssd"}},
			TagsFunc:       driveTags,
		},
		{
			PromMetricName: "weka_drive_smart_composite_temp",
			Help:           "Composite temperature of drives",
			Type:           "gauge",
			CategoryStats:  []CategoryStat{{Stat: "NVME_SMART_COMPOSITE_TEMP", Category: "ssd"}},
			TagsFunc:       driveTags,
		},
	}

	for _, def := range metricDefinitions {
		if len(def.Modes) > 0 && !slices.Contains(def.Modes, container.mode) {
			continue
		}
		var taggedValues []metrics2.TaggedValue

		if def.CustomGetValueAndTagsFunc != nil {
			taggedValues = def.CustomGetValueAndTagsFunc(ctx, container, labels)
		} else if len(def.CategoryStats) > 0 {
			for _, cs := range def.CategoryStats {
				if stats, ok := groupedMetrics[cs]; ok {
					for _, stat := range stats {
						processed := processStat(ctx, stat, container)
						value := processed.Value
						if def.ValueTransform != nil {
							value = def.ValueTransform(value)
						}
						var currentTags metrics2.TagMap = metrics2.TagMap{}
						if def.TagsFunc != nil {
							currentTags = def.TagsFunc(processed)
						}
						taggedValues = append(taggedValues, metrics2.TaggedValue{
							Tags:      util.MergeMaps(labels, currentTags),
							Value:     value,
							Timestamp: container.statsResponseLastPoll,
						})
					}
				}
			}
		}

		if len(taggedValues) > 0 {
			if def.Aggregate {
				taggedValues = aggregateTaggedValues(taggedValues)
			}
			if def.Reduce {
				taggedValues = sumTagsBy(def.ReduceBy, def.ReduceKeep, taggedValues)
			}

			response.AddMetric(metrics2.PromMetric{
				Metric: def.PromMetricName,
				Help:   def.Help,
				Type:   def.Type,
			}, taggedValues)
		}
	}
}

func sumTagsBy(sumBy string, keepTags []string, values []metrics2.TaggedValue) []metrics2.TaggedValue {
	// sum values by sumBy tag, keeping only keepTags

	getKey := func(tags []string, value metrics2.TaggedValue) string {
		var key string
		for _, tag := range tags {
			key += ":" + value.Tags[tag]
		}
		return key
	}

	// sum values by sumBy tag
	sums := make(map[string]float64)
	for _, value := range values {
		key := getKey([]string{sumBy}, value)
		sums[key] += value.Value
	}

	// last pass, keep track of processed key, if proceesed just continue, as we have sums
	ret := []metrics2.TaggedValue{}
	processed := make(map[string]bool)
	for _, value := range values {
		key := getKey(keepTags, value)
		if processed[key] {
			continue
		}
		processed[key] = true
		newValue := metrics2.TaggedValue{
			Tags:      value.Tags, // we preserve all keys, so whatever was not listed as key to sum by, will be kept, but potentially discrepancy will be created if something should have been respected as key and was not
			Value:     sums[getKey([]string{sumBy}, value)],
			Timestamp: value.Timestamp,
		}
		delete(newValue.Tags, sumBy)
		ret = append(ret, newValue)
	}

	return ret
}

func deduceFsName(ctx context.Context, container *ContainerInfo, id string) string {
	_, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	targetIdInt, err := strconv.Atoi(id)
	if err != nil {
		logger.Error(err, "Failed to convert fs id to int", "fs_id", id)
		return ""
	}
	for _, fs := range container.containerState.Result.FilesystemsSummary {
		intId, err := resources.FsIdToInteger(fs.FilesystemId)
		if err != nil {
			logger.Error(err, "Failed to convert fs id to int", "fs_id", fs.FilesystemId, "fs", fs)
			continue
		}
		if intId == targetIdInt {
			return fs.Name
		}
	}
	return ""
}

func EnsureNodeAgentSecret(ctx context.Context, mgr ctrl.Manager) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureNodeAgentSecret")
	defer end()

	secretName := config.Config.Metrics.NodeAgentSecretName
	secretNamespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: secretNamespace,
		},
		StringData: map[string]string{
			"token": util.GeneratePassword(64),
		},
	}

	kubeService := kubernetes.NewKubeService(mgr.GetClient())
	err = kubeService.EnsureSecret(ctx, secret, nil)
	if err != nil {
		logger.SetError(err, "Failed to ensure secret")
		return err
	}
	return err
}
