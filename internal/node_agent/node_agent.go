package node_agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	metrics2 "github.com/weka/weka-operator/pkg/metrics"
	"github.com/weka/weka-operator/pkg/util"
	"golang.org/x/exp/rand"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net"
	"net/http"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sync"
	"time"
)

type NodeAgent struct {
	logger         logr.Logger
	containersData containersData
	token          string
	lastTokenPull  time.Time
}

type ContainerInfo struct {
	labels                 map[string]string
	persistencePath        string
	wekaContainerName      string
	cpuInfo                LocalCpuUtilizationResponse
	cpuInfoLastPoll        time.Time
	containerState         LocalConfigStateResponse
	containerStateLastPull time.Time
	lastRegisterTimestamp  time.Time
	containerName          string
	containerId            string
	mode                   string
}

func (i *ContainerInfo) getMaxCpu() float64 {
	var maxCpu float64
	for _, cpuStats := range i.cpuInfo.Result {
		for _, cpu := range cpuStats.Cpu {
			if cpu.Stats.Utilization > maxCpu {
				maxCpu = cpu.Stats.Utilization
			}
		}
	}
	return maxCpu
}

type containersData struct {
	lock sync.RWMutex
	data map[string]*ContainerInfo
}

type RegisterContainerPayload struct {
	ContainerName     string            `json:"container_name"`
	ContainerId       string            `json:"container_id"`
	WekaContainerName string            `json:"weka_container_name"`
	PersistencePath   string            `json:"persistence_path"`
	Labels            map[string]string `json:"labels"`
	Mode              string            `json:"mode"`
}

type ProcessSummary struct {
	Up    int `json:"up"`
	Total int `json:"total"`
}

type LocalConfigStateResponse struct {
	Result struct {
		DisksSummary []struct {
			Uid          string `json:"uid"`
			SerialNumber string `json:"serial_number"`
			DevUuid      string `json:"dev_uuid"`
			Status       string `json:"status"`
			Name         string `json:"name"`
		} `json:"disks_summary"`
		HostName         string `json:"host_name"`
		HostId           string `json:"host_id"`
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

func (a *NodeAgent) Run(ctx context.Context) error {
	//TODO: Explicit server struct and handling of ctx close to call server.Shutdown
	// start http server
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", a.metricsHandler)
	mux.HandleFunc("/getContainerInfo", a.getContainerInfo) // TODO: Implement this and replace in-container-reconcile exec with polling this endpoint
	mux.HandleFunc("/register", a.registerHandler)

	bindTo := config.Config.BindAddress.NodeAgent
	a.logger.Info("Server is binding to " + bindTo)
	return http.ListenAndServe(bindTo, mux)
}

func (a *NodeAgent) metricsHandler(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "Metrics")
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
		if container.containerStateLastPull.Add(5 * time.Minute).Before(time.Now()) {
			continue
		}

		containerLabels := util.MergeMaps(container.labels,
			metrics2.TagMap{
				"container_name": container.containerName,
				"mode":           container.mode,
			})

		promResponse.AddMetric(metrics2.PromMetric{
			Metric: "weka_processes",
			Help:   "Weka processes",
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

		for nodeIdStr, cpuStats := range container.cpuInfo.Result {
			// do we care about the node id? should we report per node or containers totals? will stay with totals for now
			value := cpuStats.Cpu[0].Stats.Utilization // we expect always to get just one element in this list, as this is reuse of code that returns time series

			processId, err := resources.NodeIdToProcessId(nodeIdStr)
			if err != nil {
				a.logger.Error(err, "Failed to convert node id to process id", "node_id", nodeIdStr)
				continue
			}
			processIdStr := fmt.Sprintf("%d", processId)

			promResponse.AddMetric(metrics2.PromMetric{
				Metric: "weka_cpu_utilization",
				Help:   "Weka container CPU utilization",
			}, []metrics2.TaggedValue{
				{
					Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"process_id": processIdStr}),
					Value:     value,
					Timestamp: container.cpuInfoLastPoll,
				},
			})
		}
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
		persistencePath:   payload.PersistencePath,
		wekaContainerName: payload.WekaContainerName,
		containerName:     payload.ContainerName,
		containerId:       payload.ContainerId,
		mode:              payload.Mode,
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

	socketPath := fmt.Sprintf("%s/data/%s/container/container.sock", container.persistencePath, container.wekaContainerName)
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

	req, err := http.NewRequest("POST", "http://localhost/api/v1", bytes.NewReader(payloadBytes))
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
		Cpu []struct {
			Stats struct {
				Utilization float64 `json:"CPU_UTILIZATION"`
			} `json:"stats"`
			Timestamp string `json:"timestamp"` // format: 2024-11-28T16:03:00Z (is it parsed correctly?), more correct to use this timestamp rather then current time
		} `json:"cpu"`
	} `json:"result"`
}

func (a *NodeAgent) fetchAndPopulateMetrics(ctx context.Context, container *ContainerInfo) error {
	// WARNING: no lock here, while calling in parallel from multiple places

	ctx, _, end := instrumentation.GetLogSpan(ctx, "fetchAndPopulateMetrics")
	defer end()

	var response LocalConfigStateResponse
	err := jrpcCall(ctx, container, "get_local_config_summary", &response)
	if err != nil {
		return err
	}

	container.containerState = response
	container.containerStateLastPull = time.Now()

	var cpuResponse LocalCpuUtilizationResponse
	err = jrpcCall(ctx, container, "get_local_cpu_utilization", &cpuResponse)
	if err != nil {
		return err
	}
	container.cpuInfo = cpuResponse
	container.cpuInfoLastPoll = time.Now()

	return nil
}

type ContainerInfoResponse struct {
	ContainerMetrics weka.WekaContainerMetrics `json:"container_metrics,omitempty"`
}

type GetContainerInfoRequest struct {
	ContainerId string `json:"container_id"`
}

func (a *NodeAgent) getContainerInfo(w http.ResponseWriter, r *http.Request) {
	ctx, logger, end := instrumentation.GetLogSpan(r.Context(), "ContainerHandler")
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
	if err != nil {
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
