package node_agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/config"
	metrics2 "github.com/weka/weka-operator/pkg/metrics"
	"github.com/weka/weka-operator/pkg/util"
	"golang.org/x/exp/rand"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"
)

type NodeAgent struct {
	logger         logr.Logger
	containersData containersData
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
	//mux.HandleFunc("/containers/", a.containerHandler) // TODO: Implement this and replace in-container-reconcile exec with polling this endpoint
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

		//var min, max, avg float64
		//max = 100
		//min = 0
		cpuValues := []float64{}
		totalValue := 0.0

		for _, cpuStats := range container.cpuInfo.Result {
			// do we care about the node id? should we report per node or containers totals? will stay with totals for now
			value := cpuStats.Cpu[0].Stats.Utilization // we expect always to get just one element in this list, as this is reuse of code that returns time series
			cpuValues = append(cpuValues, value)
			totalValue += value
		}

		if len(cpuValues) == 0 {
			continue
		}

		promResponse.AddMetric(metrics2.PromMetric{
			Metric: "weka_cpu_utilization",
			Help:   "Weka container CPU utilization",
		}, []metrics2.TaggedValue{
			{
				Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"aggfunc": "avg"}),
				Value:     totalValue / float64(len(cpuValues)),
				Timestamp: container.cpuInfoLastPoll,
			},
			{
				Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"aggfunc": "max"}),
				Value:     slices.Max(cpuValues),
				Timestamp: container.cpuInfoLastPoll,
			},
			{
				Tags:      util.MergeMaps(containerLabels, metrics2.TagMap{"aggfunc": "min"}),
				Value:     slices.Min(cpuValues),
				Timestamp: container.cpuInfoLastPoll,
			},
		})
	}

	// Write the response
	writer.Header().Set("Content-Type", "text/plain")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(promResponse.String()))
}

func (a *NodeAgent) registerHandler(w http.ResponseWriter, r *http.Request) {
	//TODO: Add secret-based token auth, i.e secret created by operator and shared with node agent
	_, logger, end := instrumentation.GetLogSpan(r.Context(), "RegisterHandler")
	defer end()

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

	a.containersData.data[payload.ContainerName] = &ContainerInfo{
		labels:            payload.Labels,
		persistencePath:   payload.PersistencePath,
		wekaContainerName: payload.WekaContainerName,
		containerName:     payload.ContainerName,
		containerId:       payload.ContainerId,
		mode:              payload.Mode,
	}

	logger.Info("Container registered", "container_name", payload.ContainerName)

	response := map[string]string{"message": "Container registered successfully"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
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
