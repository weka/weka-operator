package wekacluster

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
)

const (
	statsOutputSeparator = "--stats--"
	ssOutputSeparator    = "--ss--"
)

const (
	envoyHandoverTimeout = 120 * time.Second
	// Matches the Weka upgrade's --s3-drain-timeout default.
	envoyDrainTimeout = 30 * time.Second
	// Each step's timeout counts from the start of the retirement, after the timeouts of the steps before it.
	envoyHandoverDeadline   = envoyFastDrainTimeout + envoyHandoverTimeout
	envoyRetirementDeadline = envoyHandoverDeadline + envoyDrainTimeout
)

var envoyActiveGauge = regexp.MustCompile(`(?m)^http\.ingress_http\.downstream_(rq|cx)_active:\s*(\d+)\s*$`)

// nextEnvoyStep stops at the first S3 not yet on the target image: the forwarder must exist before the next S3
// goes down. S3 containers without a node or being deleted never roll, as the rolling upgrade skips them, so they
// are skipped here too rather than blocking every later node.
func nextEnvoyStep(s3, envoys, forwarders []*weka.WekaContainer, s3Rolled func(*weka.WekaContainer) (bool, error)) (retire, s3OnNode *weka.WekaContainer, waitReason string, err error) {
	envoyByNode := containersByNode(envoys)
	forwarderByNode := containersByNode(forwarders)
	for _, container := range sortedByName(s3) {
		if !s3Placed(container) {
			continue
		}
		rolled, err := s3Rolled(container)
		if err != nil {
			return nil, nil, "", err
		}
		if !rolled {
			return nil, nil, "", nil
		}
		node := container.GetNodeAffinity()
		if envoy, ok := envoyByNode[node]; ok {
			return envoy, container, "", nil
		}
		if forwarderOnNode(forwarderByNode, node) == nil {
			return nil, nil, fmt.Sprintf("waiting for the forwarder on node %s to be created", node), nil
		}
	}
	return nil, nil, "", nil
}

// s3Placed reports whether the S3 container has a node and is not being deleted, so it takes part in the roll.
func s3Placed(s3 *weka.WekaContainer) bool {
	return s3.GetNodeAffinity() != "" && !s3.IsMarkedForDeletion()
}

// forwarderOnNode is the node's forwarder, nil when there is none or it is being deleted.
func forwarderOnNode(forwarderByNode map[weka.NodeName]*weka.WekaContainer, node weka.NodeName) *weka.WekaContainer {
	if forwarder := forwarderByNode[node]; forwarder != nil && !forwarder.IsMarkedForDeletion() {
		return forwarder
	}
	return nil
}

// s3ByRoll splits the placed S3 containers into those whose pod runs the target image and the rest.
func (r *wekaClusterReconcilerLoop) s3ByRoll(ctx context.Context, s3 []*weka.WekaContainer, targetImage string) (rolled, unrolled []*weka.WekaContainer, err error) {
	for _, container := range s3 {
		if !s3Placed(container) {
			continue
		}
		onTarget, err := r.s3OnTargetImage(ctx, container, targetImage)
		if err != nil {
			return nil, nil, err
		}
		if onTarget {
			rolled = append(rolled, container)
		} else {
			unrolled = append(unrolled, container)
		}
	}
	return rolled, unrolled, nil
}

// forwarderPinNodes are the nodes of the S3 containers that have no forwarder yet, in S3 name order. Pod affinity to
// S3 alone would let a new forwarder land on any S3 node.
func forwarderPinNodes(s3, forwarders []*weka.WekaContainer) []string {
	taken := containersByNode(forwarders)
	var nodes []string
	for _, container := range sortedByName(s3) {
		node := container.GetNodeAffinity()
		if _, ok := taken[node]; !ok {
			nodes = append(nodes, string(node))
		}
	}
	return nodes
}

// processListening reports whether `ss -ltnp` output has a listening socket owned by process.
func processListening(ss, process string) bool {
	for _, line := range strings.Split(ss, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "LISTEN") && strings.Contains(line, `("`+process+`",`) {
			return true
		}
	}
	return false
}

// envoyState is what the running envoy reports: its drain policy and uptimes from /server_info, its ingress
// gauges, and whether it still listens on the LB port.
type envoyState struct {
	immediate             bool
	epochUptime, uptime   time.Duration
	requests, connections int
	listening             bool
}

// fastDrainReady reports whether envoy runs the immediate drain policy and the hot-restarted parent, which exits
// envoyParentExitWait after the restart, is gone.
func (s envoyState) fastDrainReady() bool {
	return s.immediate && s.epochUptime >= envoyParentExitWait
}

func (s envoyState) drained() bool {
	return s.requests == 0 && s.connections == 0 && !s.listening
}

func (s envoyState) String() string {
	return fmt.Sprintf("%d active requests, %d open connections, listening: %t", s.requests, s.connections, s.listening)
}

type envoyServerInfo struct {
	UptimeCurrentEpoch string `json:"uptime_current_epoch"`
	UptimeAllEpochs    string `json:"uptime_all_epochs"`
	CommandLineOptions struct {
		DrainStrategy string `json:"drain_strategy"`
		DrainTime     string `json:"drain_time"`
	} `json:"command_line_options"`
}

// parseEnvoyState reads /server_info, statsOutputSeparator, the ingress gauges, ssOutputSeparator and `ss -ltnp`
// for the LB port.
func parseEnvoyState(out string) (envoyState, error) {
	serverInfo, rest, found := strings.Cut(out, statsOutputSeparator)
	if !found {
		return envoyState{}, errors.Errorf("no stats after envoy server_info: %q", out)
	}
	stats, ss, found := strings.Cut(rest, ssOutputSeparator)
	if !found {
		return envoyState{}, errors.Errorf("no ss output after envoy stats: %q", out)
	}

	var info envoyServerInfo
	if err := json.Unmarshal([]byte(serverInfo), &info); err != nil {
		return envoyState{}, errors.Wrap(err, "parse envoy server_info")
	}
	drainTime, err := time.ParseDuration(info.CommandLineOptions.DrainTime)
	if err != nil {
		return envoyState{}, errors.Wrapf(err, "parse envoy drain_time %q", info.CommandLineOptions.DrainTime)
	}
	var state envoyState
	if state.epochUptime, err = time.ParseDuration(info.UptimeCurrentEpoch); err != nil {
		return envoyState{}, errors.Wrapf(err, "parse envoy uptime_current_epoch %q", info.UptimeCurrentEpoch)
	}
	if state.uptime, err = time.ParseDuration(info.UptimeAllEpochs); err != nil {
		return envoyState{}, errors.Wrapf(err, "parse envoy uptime_all_epochs %q", info.UptimeAllEpochs)
	}
	state.immediate = strings.EqualFold(info.CommandLineOptions.DrainStrategy, fastDrainImmediate) && drainTime <= envoyMaxFastDrain

	gauges := map[string]int{}
	for _, m := range envoyActiveGauge.FindAllStringSubmatch(stats, -1) {
		v, err := strconv.Atoi(m[2])
		if err != nil {
			return envoyState{}, err
		}
		gauges[m[1]] = v
	}
	if len(gauges) != 2 {
		return envoyState{}, errors.Errorf("missing downstream_rq_active or downstream_cx_active in envoy stats: %q", stats)
	}
	state.requests, state.connections = gauges["rq"], gauges["cx"]
	state.listening = processListening(ss, "envoy")
	return state, nil
}

// s3OnTargetImage reports whether the S3 pod itself runs the target image. LastAppliedImage is not usable
// here: it is set only once the pod is ready, and the new S3 cannot become ready while the old envoy holds
// the LB port.
func (r *wekaClusterReconcilerLoop) s3OnTargetImage(ctx context.Context, s3 *weka.WekaContainer, targetImage string) (bool, error) {
	if s3.Spec.Image != targetImage {
		return false, nil
	}
	pod, err := r.podOf(ctx, s3)
	if err != nil || pod == nil || pod.DeletionTimestamp != nil {
		return false, err
	}
	podContainer, err := resources.GetWekaPodContainer(pod)
	if err != nil {
		return false, err
	}
	return podContainer.Image == targetImage, nil
}

// retireEnvoysForForwarder replaces the envoys with forwarders one node at a time, behind the S3 roll.
// It returns a wait error while an envoy is being retired or a forwarder is missing.
func (r *wekaClusterReconcilerLoop) retireEnvoysForForwarder(ctx context.Context, s3, envoys, forwarders []*weka.WekaContainer, targetImage string) error {
	retire, s3OnNode, waitReason, err := nextEnvoyStep(s3, envoys, forwarders, func(c *weka.WekaContainer) (bool, error) {
		return r.s3OnTargetImage(ctx, c, targetImage)
	})
	if err != nil {
		return err
	}
	if waitReason != "" {
		return lifecycle.NewWaitError(errors.New(waitReason))
	}
	if retire == nil {
		return nil
	}
	if err := r.setForwarderEra(ctx, s3OnNode); err != nil {
		return err
	}
	return r.retireEnvoy(ctx, retire, s3OnNode, forwarders)
}

// setForwarderEra switches the S3 container to forwarder mode; without it the new S3 neither binds the LB
// port nor autoconfigures the forwarder. It is issued before the envoy is drained, as the S3 waits for the
// port, and is idempotent.
func (r *wekaClusterReconcilerLoop) setForwarderEra(ctx context.Context, s3 *weka.WekaContainer) error {
	if s3.Status.ClusterContainerID == nil {
		return lifecycle.NewWaitError(errors.Errorf("S3 container %s has no cluster container id yet", s3.Name))
	}
	return errors.Wrapf(
		services.NewWekaService(r.ExecService, s3).SetS3FwdEra(ctx, *s3.Status.ClusterContainerID),
		"enabling forwarder era on %s", s3.Name)
}

// forwarderAdminSocket is where a forwarder serves its admin API until it can bind the admin port, which the
// envoy holds. The directory is shared by the pods on a node.
const forwarderAdminSocket = "/opt/weka/forwarder/admin.sock"

type forwarderReady struct {
	Covering  string `json:"covering"`
	Healthy   bool   `json:"healthy"`
	ProbeLive bool   `json:"probeLive"`
}

func (f forwarderReady) idleAndHealthy() bool {
	return f.Covering == "idle" && f.Healthy && f.ProbeLive
}

// awaitForwarderBesideEnvoy waits until the forwarder on the envoy's node, created once the S3 there runs the
// target image, is idle and healthy on its admin socket, as the Weka upgrade does before it drains the envoy.
func (r *wekaClusterReconcilerLoop) awaitForwarderBesideEnvoy(ctx context.Context, envoy, s3, forwarder *weka.WekaContainer) error {
	if forwarder == nil {
		return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for the forwarder on node %s to be created", s3.GetNodeAffinity()), 3*time.Second)
	}
	ready, err := r.forwarderReadyOnSocket(ctx, forwarder)
	if err != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(err, "reading /ready of forwarder %s", forwarder.Name), 5*time.Second)
	}
	if !ready.idleAndHealthy() {
		return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for forwarder %s to be idle and healthy: %+v", forwarder.Name, ready), 3*time.Second)
	}
	r.reportOnce(envoy, v1.EventTypeNormal, "ForwarderReadyBesideEnvoy",
		fmt.Sprintf("forwarder %s is idle and healthy beside envoy %s", forwarder.Name, envoy.Name))
	return nil
}

func (r *wekaClusterReconcilerLoop) forwarderReadyOnSocket(ctx context.Context, forwarder *weka.WekaContainer) (forwarderReady, error) {
	out, err := r.execIn(ctx, forwarder, "ForwarderReady", 10*time.Second,
		"curl", "-sSf", "--max-time", "3", "--unix-socket", forwarderAdminSocket, "http://localhost/ready")
	if err != nil {
		return forwarderReady{}, err
	}
	var ready forwarderReady
	if err := json.Unmarshal([]byte(out), &ready); err != nil {
		return forwarderReady{}, errors.Wrapf(err, "parse forwarder /ready %q", out)
	}
	return ready, nil
}

func (r *wekaClusterReconcilerLoop) execIn(ctx context.Context, container *weka.WekaContainer, name string, timeout time.Duration, cmd ...string) (string, error) {
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, container, &timeout)
	if err != nil {
		return "", errors.Wrapf(err, "failed to get executor for %s", container.Name)
	}
	stdout, stderr, err := executor.ExecNamed(ctx, name, cmd)
	if err != nil {
		return "", errors.Wrapf(err, "%s in %s failed: %s", name, container.Name, stderr.String())
	}
	return stdout.String(), nil
}

// awaitS3Handover waits for wekas3 to listen on the LB port beside the envoy, so draining the envoy does not
// leave the port dark. It gives up at envoyHandoverDeadline into the retirement.
func (r *wekaClusterReconcilerLoop) awaitS3Handover(ctx context.Context, envoy, s3 *weka.WekaContainer, age time.Duration) error {
	listening, err := r.s3ListeningOnLbPort(ctx, s3)
	if err == nil && listening {
		r.reportOnce(envoy, v1.EventTypeNormal, "EnvoyHandoverConfirmed",
			fmt.Sprintf("wekas3 on %s listens on the LB port beside envoy %s", s3.Name, envoy.Name))
		return nil
	}
	if age >= envoyHandoverDeadline {
		r.reportOnce(envoy, v1.EventTypeWarning, "EnvoyHandoverTimedOut",
			fmt.Sprintf("wekas3 on %s did not bind the LB port beside envoy %s, draining anyway (last check: %s)", s3.Name, envoy.Name, handoverCheck(listening, err)))
		return nil
	}
	if err != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(err, "checking wekas3 listener on %s", s3.Name), 5*time.Second)
	}
	return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for wekas3 on %s to listen on the LB port", s3.Name), 3*time.Second)
}

func handoverCheck(listening bool, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("listening=%t", listening)
}

func (r *wekaClusterReconcilerLoop) s3ListeningOnLbPort(ctx context.Context, s3 *weka.WekaContainer) (bool, error) {
	out, err := r.execIn(ctx, s3, "CheckS3LbListener", 10*time.Second,
		"ss", "-ltnp", fmt.Sprintf("sport = :%d", r.cluster.Status.Ports.LbPort))
	if err != nil {
		return false, err
	}
	return processListening(out, "wekas3"), nil
}

// retireEnvoy runs the steps that remain for the envoy, as the envoy, its S3 and the forwarder report them, then
// deletes it. Once envoy stops listening the drain is under way and the earlier gates are not checked again: the
// forwarder may take traffic as the envoy drains.
func (r *wekaClusterReconcilerLoop) retireEnvoy(ctx context.Context, envoy, s3 *weka.WekaContainer, forwarders []*weka.WekaContainer) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	if envoy.IsMarkedForDeletion() {
		return lifecycle.NewWaitError(errors.Errorf("envoy %s is being deleted", envoy.Name))
	}

	forwarder := forwarderOnNode(containersByNode(forwarders), s3.GetNodeAffinity())
	state, stateErr := r.envoyState(ctx, envoy)
	age, err := r.envoyRetirementAge(ctx, s3, forwarder)
	if err != nil {
		return err
	}
	if stateErr == nil {
		age = min(age, state.uptime)
	}
	if stateErr != nil {
		// Past the deadline the envoy goes regardless of what it would report, but not before its replacement is ready.
		if age >= envoyRetirementDeadline {
			if err := r.awaitForwarderBesideEnvoy(ctx, envoy, s3, forwarder); err != nil {
				return err
			}
			logger.Error(stateErr, "Envoy unreadable past the retirement deadline, deleting it anyway", "envoy", envoy.Name)
			return r.deleteRetiredEnvoy(ctx, envoy, false)
		}
		return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(stateErr, "reading envoy %s", envoy.Name), 5*time.Second)
	}

	if state.listening {
		if err := r.awaitFastDrain(ctx, envoy, state, age); err != nil {
			return err
		}
		if err := r.awaitForwarderBesideEnvoy(ctx, envoy, s3, forwarder); err != nil {
			return err
		}
		if err := r.awaitS3Handover(ctx, envoy, s3, age); err != nil {
			return err
		}
	}

	// A repeated drain request does not restart a running graceful drain, so it is sent while envoy listens.
	if !state.drained() {
		var drainErr error
		if state.listening {
			drainErr = r.sendEnvoyDrain(ctx, envoy, state.immediate)
		}
		if age < envoyRetirementDeadline {
			if drainErr != nil {
				return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(drainErr, "draining envoy %s", envoy.Name), 5*time.Second)
			}
			return lifecycle.NewWaitErrorWithDuration(errors.Errorf("envoy %s still draining: %s", envoy.Name, state), 2*time.Second)
		}
		logger.Warn("Envoy drain timed out, deleting it anyway", "envoy", envoy.Name, "state", state.String(), "error", drainErr)
	}
	return r.deleteRetiredEnvoy(ctx, envoy, state.immediate)
}

// envoyRetirementAge is how long the envoy's retirement has run, measured from what the cluster records rather
// than stored, so an operator restart or failover does not reset it: since the latest start of the S3 container on
// the target image and the forwarder. The caller also bounds it by the envoy's uptime, since a restarted envoy runs
// the stock drain policy and listens again. A forwarder still being created must not use up the later steps' time,
// so it is 0 while the S3 or forwarder container is missing or not running, and no timeout removes the envoy before
// its replacement runs.
func (r *wekaClusterReconcilerLoop) envoyRetirementAge(ctx context.Context, s3, forwarder *weka.WekaContainer) (time.Duration, error) {
	var age time.Duration
	for _, container := range []*weka.WekaContainer{s3, forwarder} {
		running, err := r.runningFor(ctx, container)
		if err != nil || running == 0 {
			return 0, err
		}
		if age == 0 || running < age {
			age = running
		}
	}
	return age, nil
}

// runningFor is how long the container's weka container has been running, 0 when it is not or container is nil.
func (r *wekaClusterReconcilerLoop) runningFor(ctx context.Context, container *weka.WekaContainer) (time.Duration, error) {
	if container == nil {
		return 0, nil
	}
	pod, err := r.podOf(ctx, container)
	if err != nil || pod == nil {
		return 0, err
	}
	status, err := resources.GetWekaPodContainerStatus(pod)
	if err != nil {
		return 0, err
	}
	if status.State.Running == nil {
		return 0, nil
	}
	return time.Since(status.State.Running.StartedAt.Time), nil
}

// podOf returns the container's pod, nil when there is none.
func (r *wekaClusterReconcilerLoop) podOf(ctx context.Context, container *weka.WekaContainer) (*v1.Pod, error) {
	pod := &v1.Pod{}
	if err := r.getClient().Get(ctx, client.ObjectKeyFromObject(container), pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "failed to get pod of %s", container.Name)
	}
	return pod, nil
}

func (r *wekaClusterReconcilerLoop) deleteRetiredEnvoy(ctx context.Context, envoy *weka.WekaContainer, graceful bool) error {
	_ = r.RecordEvent(v1.EventTypeNormal, "EnvoyRetired", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
		fmt.Sprintf("Deleting envoy %s: its S3 container runs the forwarder-capable image (graceful drain: %t)", envoy.Name, graceful))
	if err := r.getClient().Delete(ctx, envoy); err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrapf(err, "failed to delete envoy %s", envoy.Name)
	}
	return lifecycle.NewWaitError(errors.Errorf("envoy %s deleted, waiting for its forwarder", envoy.Name))
}

// sendEnvoyDrain stops envoy's listeners, gracefully only when the immediate drain policy is in effect (the
// shipped policy is a 600s gradual drain). Envoy runs on the host network, so its admin API is on loopback.
func (r *wekaClusterReconcilerLoop) sendEnvoyDrain(ctx context.Context, envoy *weka.WekaContainer, graceful bool) error {
	drainURL := r.envoyAdminURL() + "/drain_listeners"
	if graceful {
		drainURL += "?graceful"
	}
	_, err := r.execIn(ctx, envoy, "DrainEnvoy", 10*time.Second,
		"curl", "-sSf", "-o", "/dev/null", "-X", "POST", drainURL)
	return err
}

// envoyState reads envoy's /server_info and ingress gauges, and whether it listens on the LB port. A graceful drain
// keeps accepting connections for drain_time, so in-flight requests alone reaching zero is not enough: a client
// that reconnects in that window is reset when envoy is deleted. ss runs in envoy's pod on the host network.
func (r *wekaClusterReconcilerLoop) envoyState(ctx context.Context, envoy *weka.WekaContainer) (envoyState, error) {
	admin := r.envoyAdminURL()
	cmd := fmt.Sprintf(`curl -sSf '%s/server_info' && echo %s && curl -sSf '%s/stats?filter=http.ingress_http.downstream_' && echo %s && ss -ltnp 'sport = :%d'`,
		admin, statsOutputSeparator, admin, ssOutputSeparator, r.cluster.Status.Ports.LbPort)
	out, err := r.execIn(ctx, envoy, "EnvoyState", 10*time.Second, "bash", "-ce", cmd)
	if err != nil {
		return envoyState{}, err
	}
	return parseEnvoyState(out)
}

// reportOnce records the event for the envoy once per retirement: the Throttler is per cluster and outlives the
// reconcile.
func (r *wekaClusterReconcilerLoop) reportOnce(envoy *weka.WekaContainer, eventType, reason, message string) {
	_ = r.RecordEventThrottledKeyed(eventType, reason, envoy.Name, consts.ActionUpgrade, message, time.Hour) //nolint:errcheck // event recording errors are intentionally ignored
}

// retireEnvoysAtOnce removes the envoys without a drain for an all-at-once upgrade, where S3 is down anyway. Each
// goes once the S3 on its node runs the target image, after the forwarder era is set for that S3.
func (r *wekaClusterReconcilerLoop) retireEnvoysAtOnce(ctx context.Context, s3, envoys []*weka.WekaContainer, targetImage string) error {
	s3ByNode := containersByNode(s3)
	pending := 0
	for _, envoy := range envoys {
		if envoy.IsMarkedForDeletion() {
			continue
		}
		if s3OnNode, ok := s3ByNode[envoy.GetNodeAffinity()]; ok {
			rolled, err := r.s3OnTargetImage(ctx, s3OnNode, targetImage)
			if err != nil {
				return err
			}
			if !rolled {
				pending++
				continue
			}
			if err := r.setForwarderEra(ctx, s3OnNode); err != nil {
				return err
			}
		}
		if err := r.getClient().Delete(ctx, envoy); err != nil && !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to delete envoy %s", envoy.Name)
		}
	}
	if pending > 0 {
		return lifecycle.NewWaitError(errors.Errorf("waiting for %d S3 containers to run the target image before removing their envoys", pending))
	}
	return nil
}
