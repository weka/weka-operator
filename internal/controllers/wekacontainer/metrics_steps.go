// This file contains functions related to metrics reporting during WekaContainer reconciliation
package wekacontainer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/attribute"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/pkg/util"
)

func MetricsSteps(loop *containerReconcilerLoop) []lifecycle.Step {
	container := loop.container

	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Name: "SetStatusMetrics",
			Run:  loop.SetStatusMetrics,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
				lifecycle.IsNotFunc(loop.PodNotSet),
				func() bool {
					return slices.Contains(
						[]string{
							weka.WekaContainerModeCompute,
							weka.WekaContainerModeClient,
							weka.WekaContainerModeS3,
							weka.WekaContainerModeNfs,
							weka.WekaContainerModeDrive,
							// TODO: Expand to clients, introduce API-level(or not) HasManagement check
						}, container.Spec.Mode)
				},
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Config.Metrics.Containers.PollingRate,
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Name: "RegisterContainerOnMetrics",
			Run:  loop.RegisterContainerOnMetrics,
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
				func() bool {
					return slices.Contains(
						[]string{
							weka.WekaContainerModeCompute,
							weka.WekaContainerModeClient,
							weka.WekaContainerModeS3,
							weka.WekaContainerModeNfs,
							weka.WekaContainerModeDrive,
							weka.WekaContainerModeEnvoy,
						}, container.Spec.Mode)
				},
			},
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			Name: "ReportOtelMetrics",
			Run:  loop.ReportOtelMetrics,
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(loop.PodNotSet),
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Config.Metrics.Containers.PollingRate,
			},
			ContinueOnError: true,
		},
	}
}

func (r *containerReconcilerLoop) SetStatusMetrics(ctx context.Context) error {
	// TODO: Should we be do this locally? it actually will be better to find failures from different container
	// but, if we dont keep locality - performance wise too easy to make mistake and funnel everything throught just one
	// tldr: we need a proper service gateway for weka api, that will both healthcheck and distribute
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// submit http request to metrics pod
	agentPod, err := r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		return err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	payload := node_agent.GetContainerInfoRequest{ContainerId: string(r.container.GetUID())}

	url := "http://" + agentPod.Status.PodIP + ":8090/getContainerInfo"

	// Convert the payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "Error marshalling payload")
	}

	// limit the getContainerInfo request
	timeout := config.Config.Metrics.Containers.RequestsTimeouts.GetContainerInfo
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("getContainerInfo failed: %s, status: %d", string(body), resp.StatusCode)
		return err
	}

	var response node_agent.ContainerInfoResponse
	err = json.NewDecoder(bytes.NewReader(body)).Decode(&response)
	if err != nil {
		logger.Error(err, "Error decoding response body", "body", string(body))
		return err
	}

	patch := client.MergeFrom(r.container.DeepCopy())

	r.container.Status.Stats = &response.ContainerMetrics
	if r.container.Status.PrinterColumns == nil {
		r.container.Status.PrinterColumns = &weka.ContainerPrinterColumns{}
	}

	activeMounts, _ := r.getCachedActiveMounts(ctx)

	if r.container.HasFrontend() && activeMounts != nil {
		r.container.Status.PrinterColumns.ActiveMounts = weka.StringMetric(fmt.Sprintf("%d", *activeMounts))
		r.container.Status.Stats.ActiveMounts = weka.IntMetric(int64(*activeMounts))
	}

	r.container.Status.Stats.Processes.Desired = weka.IntMetric(int64(r.container.Spec.NumCores) + 1)
	if r.container.IsDriveContainer() {
		r.container.Status.Stats.Drives.DriveCounters.Desired = weka.IntMetric(int64(r.container.Spec.NumDrives))
		r.container.Status.PrinterColumns.Drives = weka.StringMetric(r.container.Status.Stats.Drives.DriveCounters.String())
	}
	r.container.Status.PrinterColumns.Processes = weka.StringMetric(r.container.Status.Stats.Processes.String())
	r.container.Status.Stats.LastUpdate = metav1.NewTime(time.Now())

	TracedPatch := func() error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "PatchContainerStatus")
		defer end()
		ret := r.Status().Patch(ctx, r.container, patch)
		if ret != nil {
			logger.SetError(ret, "Error patching container status")
		}
		return nil
	}

	return TracedPatch()
}

func (r *containerReconcilerLoop) RegisterContainerOnMetrics(ctx context.Context) error {
	podStatus, podStatusStartTime := r.getPodStatusInfo()
	throttlingKey := fmt.Sprintf("RegisterContainerOnMetrics-%s", podStatus)
	throttler := r.ThrottlingMap.WithPartition("container/" + r.container.Name)

	if !throttler.ShouldRun(throttlingKey, &throttling.ThrottlingSettings{
		Interval:                    config.Config.Metrics.Containers.PollingRate,
		DisableRandomPreSetInterval: true,
	}) {
		return nil
	}

	scrapeTargets := []node_agent.ScrapeTarget{}

	if r.container.Spec.Mode == weka.WekaContainerModeEnvoy {
		if r.container.Spec.ExposedPorts != nil {
			for _, port := range r.container.Spec.ExposedPorts {
				if port.Name == "envoy-admin" {
					scrapeTargets = append(scrapeTargets, node_agent.ScrapeTarget{
						Port:    int(port.ContainerPort),
						Path:    "/stats/prometheus",
						AppName: "weka_s3_envoy",
					})
				}
			}
		}
	}

	// find a pod service node metrics
	payload := node_agent.RegisterContainerPayload{
		ContainerName:      r.container.Name,
		ContainerId:        string(r.container.GetUID()),
		WekaContainerName:  r.container.Spec.WekaContainerName,
		Labels:             r.container.GetLabels(),
		Mode:               r.container.Spec.Mode,
		ScrapeTargets:      scrapeTargets,
		PodStatus:          podStatus,
		PodStatusStartTime: podStatusStartTime,
	}
	// submit http request to metrics pod
	agentPod, err := r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		return err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	url := "http://" + agentPod.Status.PodIP + ":8090/register"

	// Convert the payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "Error marshalling payload")
	}

	// limit the register request
	timeout := config.Config.Metrics.Containers.RequestsTimeouts.Register
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(ctx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return errors.Wrap(err, "Error sending register request")
	}

	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("error sending register request, status: " + resp.Status)
	}

	throttler.SetNow(throttlingKey)

	return nil
}

func (r *containerReconcilerLoop) ReportOtelMetrics(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MetricsData", "pod_name", r.container.Name, "namespace", r.container.Namespace, "mode", r.container.Spec.Mode)
	defer end()

	if r.MetricsService == nil {
		logger.Warn("Metrics service is not set")
		return nil
	}
	if r.pod == nil {
		return errors.New("on pod is set")
	}

	metrics, err := r.MetricsService.GetPodMetrics(ctx, r.pod)
	if err != nil {
		logger.Warn("Error getting pod metrics", "error", err)
		return nil // we ignore error, as this is not mandatory functionality
	}

	logger.SetAttributes(
		attribute.Float64("cpu_usage", metrics.CpuUsage),
		attribute.Int64("memory_usage", metrics.MemoryUsage),
		attribute.Float64("cpu_request", metrics.CpuRequest),
		attribute.Int64("memory_request", metrics.MemoryRequest),
		attribute.Float64("cpu_limit", metrics.CpuLimit),
		attribute.Int64("memory_limit", metrics.MemoryLimit),
	)

	if r.container.Status.Stats == nil {
		return nil
	}

	if r.container.IsBackend() {
		logger.SetAttributes(
			attribute.Int64("desired_processes", int64(r.container.Status.Stats.Processes.Desired)),
			attribute.Int64("created_processes", int64(r.container.Status.Stats.Processes.Desired)),
			attribute.Int64("active_processes", int64(r.container.Status.Stats.Processes.Active)),
		)
	}
	if r.container.IsDriveContainer() {
		logger.SetAttributes(
			attribute.Int64("desired_drives", int64(r.container.Status.Stats.Drives.DriveCounters.Desired)),
			attribute.Int64("created_drives", int64(r.container.Status.Stats.Drives.DriveCounters.Created)),
			attribute.Int64("active_drives", int64(r.container.Status.Stats.Drives.DriveCounters.Active)),
		)
	}

	return nil
}

func (r *containerReconcilerLoop) getPodStatusInfo() (string, time.Time) {
	if r.pod == nil {
		return "Unknown", time.Now()
	}

	pod := r.pod
	var statusStartTime time.Time
	currentStatus := string(pod.Status.Phase)

	if pod.DeletionTimestamp != nil {
		currentStatus = "Terminating"
		statusStartTime = pod.DeletionTimestamp.Time
		// Adjust statusStartTime to when termination actually started if grace period > 0
		if pod.Spec.TerminationGracePeriodSeconds != nil && *pod.Spec.TerminationGracePeriodSeconds > 0 {
			gracePeriod := time.Duration(*pod.Spec.TerminationGracePeriodSeconds) * time.Second
			statusStartTime = statusStartTime.Add(-gracePeriod)
		}
	} else {
		switch pod.Status.Phase {
		case v1.PodPending:
			statusStartTime = pod.CreationTimestamp.Time
			for _, cond := range pod.Status.Conditions {
				if cond.Type == v1.PodScheduled && cond.Status == v1.ConditionTrue {
					if !cond.LastTransitionTime.IsZero() {
						statusStartTime = cond.LastTransitionTime.Time
					}
				}
			}
		case v1.PodRunning:
			for _, cond := range pod.Status.Conditions {
				if cond.Type == v1.PodReady && cond.Status == v1.ConditionTrue {
					if !cond.LastTransitionTime.IsZero() {
						statusStartTime = cond.LastTransitionTime.Time
					} else {
						statusStartTime = pod.CreationTimestamp.Time
					}
					break
				}
			}
			if statusStartTime.IsZero() {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == v1.ContainersReady && cond.Status == v1.ConditionTrue {
						if !cond.LastTransitionTime.IsZero() {
							statusStartTime = cond.LastTransitionTime.Time
						}
						break
					}
				}
			}
			if statusStartTime.IsZero() {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == v1.PodInitialized && cond.Status == v1.ConditionTrue {
						if !cond.LastTransitionTime.IsZero() {
							statusStartTime = cond.LastTransitionTime.Time
						}
						break
					}
				}
			}
			if statusStartTime.IsZero() {
				statusStartTime = pod.CreationTimestamp.Time
			}
		case v1.PodSucceeded, v1.PodFailed:
			for _, cond := range pod.Status.Conditions {
				if cond.Type == v1.PodReady && cond.Status == v1.ConditionFalse {
					if !cond.LastTransitionTime.IsZero() {
						statusStartTime = cond.LastTransitionTime.Time
					}
					break
				}
			}
			if statusStartTime.IsZero() {
				statusStartTime = pod.CreationTimestamp.Time
			}
		default:
			statusStartTime = pod.CreationTimestamp.Time
		}
	}

	if statusStartTime.IsZero() {
		statusStartTime = pod.CreationTimestamp.Time
	}

	return currentStatus, statusStartTime
}

func (r *containerReconcilerLoop) DeregisterContainerFromMetrics(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DeregisterContainerFromMetrics")
	defer end()

	// Get the node agent pod
	agentPod, err := r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		// If node agent pod not found, the container is already cleaned up
		if strings.Contains(err.Error(), "not found") {
			logger.Info("Node agent pod not found, container already cleaned up")
			return nil
		}
		return err
	}

	// Read the node agent token
	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return err
	}

	// Create payload for deregistration
	payload := node_agent.DeregisterContainerPayload{
		ContainerId: string(r.container.UID),
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Send deregistration request to node agent
	url := "http://" + agentPod.Status.PodIP + ":8090/deregister"
	timeout := config.Config.Metrics.Containers.RequestsTimeouts.Register // Reuse register timeout

	logger.Info("Deregistering container from metrics", "container_id", payload.ContainerId, "node_agent_url", url)

	// Create context with timeout for the request
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(reqCtx, url, jsonData, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		// Don't fail if deregistration fails - it's cleanup, not critical
		logger.Warn("Failed to deregister container from metrics", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warn("Node agent returned non-200 status for deregistration", "status", resp.StatusCode)
		return nil
	}

	logger.Info("Container successfully deregistered from metrics")
	return nil
}
