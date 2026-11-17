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

const ssOutputSeparator = "--ss--"

const (
	envoyHandoverTimeout = 120 * time.Second
	// Matches the Weka upgrade's --s3-drain-timeout default.
	envoyDrainTimeout = 30 * time.Second
)

var envoyActiveGauge = regexp.MustCompile(`(?m)^http\.ingress_http\.downstream_(rq|cx)_active:\s*(\d+)\s*$`)

// nextEnvoyStep stops at the first S3 not yet on the target image: the forwarder must exist before the next S3
// goes down.
func nextEnvoyStep(s3, envoys, forwarders []*weka.WekaContainer, s3Rolled func(*weka.WekaContainer) (bool, error)) (retire, s3OnNode *weka.WekaContainer, waitReason string, err error) {
	envoyByNode := containersByNode(envoys)
	forwarderByNode := containersByNode(forwarders)
	for _, container := range sortedByName(s3) {
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
		if _, ok := forwarderOnNode(forwarderByNode, node); !ok {
			return nil, nil, fmt.Sprintf("waiting for the forwarder on node %s to be created", node), nil
		}
	}
	return nil, nil, "", nil
}

func forwarderOnNode(forwarderByNode map[weka.NodeName]*weka.WekaContainer, node weka.NodeName) (*weka.WekaContainer, bool) {
	forwarder, ok := forwarderByNode[node]
	return forwarder, ok && !forwarder.IsMarkedForDeletion()
}

// envoysWithoutForwarder are the envoys whose node does not get a forwarder yet.
func envoysWithoutForwarder(envoys []*weka.WekaContainer) []*weka.WekaContainer {
	var result []*weka.WekaContainer
	for _, envoy := range envoys {
		if _, ok := envoy.Annotations[consts.AnnotationEnvoyForwarder]; !ok {
			result = append(result, envoy)
		}
	}
	return result
}

// forwarderPinNodes are the nodes of S3 containers that have no off-path sidecar yet, in S3 name order. Pod
// affinity to S3 alone would let a new forwarder land on any S3 node.
func forwarderPinNodes(s3, sidecars []*weka.WekaContainer) []string {
	taken := containersByNode(sidecars)
	var nodes []string
	for _, container := range sortedByName(s3) {
		node := container.GetNodeAffinity()
		if node == "" {
			continue
		}
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

type envoyDrainState struct {
	requests, connections int
	listening             bool
}

func (d envoyDrainState) done() bool {
	return d.requests == 0 && d.connections == 0 && !d.listening
}

func (d envoyDrainState) String() string {
	return fmt.Sprintf("%d active requests, %d open connections, listening: %t", d.requests, d.connections, d.listening)
}

// parseEnvoyDrainState reads envoy's ingress gauges followed by ssOutputSeparator and `ss -ltnp` for the LB port.
func parseEnvoyDrainState(out string) (envoyDrainState, error) {
	stats, ss, found := strings.Cut(out, ssOutputSeparator)
	if !found {
		return envoyDrainState{}, errors.Errorf("no ss output after envoy stats: %q", out)
	}
	gauges := map[string]int{}
	for _, m := range envoyActiveGauge.FindAllStringSubmatch(stats, -1) {
		v, err := strconv.Atoi(m[2])
		if err != nil {
			return envoyDrainState{}, err
		}
		gauges[m[1]] = v
	}
	if len(gauges) != 2 {
		return envoyDrainState{}, errors.Errorf("missing downstream_rq_active or downstream_cx_active in envoy stats: %q", stats)
	}
	return envoyDrainState{requests: gauges["rq"], connections: gauges["cx"], listening: processListening(ss, "envoy")}, nil
}

// s3OnTargetImage reports whether the S3 pod itself runs the target image. LastAppliedImage is not usable
// here: it is set only once the pod is ready, and the new S3 cannot become ready while the old envoy holds
// the LB port.
func (r *wekaClusterReconcilerLoop) s3OnTargetImage(ctx context.Context, s3 *weka.WekaContainer, targetImage string) (bool, error) {
	if s3.Spec.Image != targetImage {
		return false, nil
	}
	pod := &v1.Pod{}
	if err := r.getClient().Get(ctx, client.ObjectKeyFromObject(s3), pod); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, errors.Wrapf(err, "failed to get pod of %s", s3.Name)
	}
	if pod.DeletionTimestamp != nil {
		return false, nil
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
	if _, drained := retire.Annotations[consts.AnnotationEnvoyDrainStarted]; !drained {
		if err := r.awaitFastDrain(ctx, retire); err != nil {
			return err
		}
		if err := r.awaitForwarderBesideEnvoy(ctx, retire, s3OnNode, forwarders); err != nil {
			return err
		}
		if err := r.awaitS3Handover(ctx, retire, s3OnNode); err != nil {
			return err
		}
	}
	return r.retireEnvoy(ctx, retire)
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

const (
	forwarderWantedValue = "wanted"
	forwarderReadyValue  = "ready"
)

type forwarderReady struct {
	Covering  string `json:"covering"`
	Healthy   bool   `json:"healthy"`
	ProbeLive bool   `json:"probeLive"`
}

func (f forwarderReady) idleAndHealthy() bool {
	return f.Covering == "idle" && f.Healthy && f.ProbeLive
}

// awaitForwarderBesideEnvoy has the forwarder created on the envoy's node and waits until it is idle and healthy
// on its admin socket, as the Weka upgrade does before it drains the envoy.
func (r *wekaClusterReconcilerLoop) awaitForwarderBesideEnvoy(ctx context.Context, envoy, s3 *weka.WekaContainer, forwarders []*weka.WekaContainer) error {
	switch envoy.Annotations[consts.AnnotationEnvoyForwarder] {
	case forwarderReadyValue:
		return nil
	case "":
		if err := r.setEnvoyAnnotation(ctx, envoy, consts.AnnotationEnvoyForwarder, forwarderWantedValue); err != nil {
			return err
		}
	}

	node := s3.GetNodeAffinity()
	forwarder, ok := forwarderOnNode(containersByNode(forwarders), node)
	if !ok {
		return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for the forwarder on node %s to be created", node), 3*time.Second)
	}
	ready, err := r.forwarderReadyOnSocket(ctx, forwarder)
	if err != nil {
		return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(err, "reading /ready of forwarder %s", forwarder.Name), 5*time.Second)
	}
	if !ready.idleAndHealthy() {
		return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for forwarder %s to be idle and healthy: %+v", forwarder.Name, ready), 3*time.Second)
	}
	_ = r.RecordEvent(v1.EventTypeNormal, "ForwarderReadyBesideEnvoy", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
		fmt.Sprintf("forwarder %s is idle and healthy beside envoy %s", forwarder.Name, envoy.Name))
	return r.setEnvoyAnnotation(ctx, envoy, consts.AnnotationEnvoyForwarder, forwarderReadyValue)
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
// leave the port dark. It gives up after envoyHandoverTimeout.
func (r *wekaClusterReconcilerLoop) awaitS3Handover(ctx context.Context, envoy, s3 *weka.WekaContainer) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	started, _, err := r.annotateStart(ctx, envoy, consts.AnnotationEnvoyHandoverStarted)
	if err != nil {
		return err
	}

	listening, err := r.s3ListeningOnLbPort(ctx, s3)
	if err != nil {
		return errors.Wrapf(err, "checking wekas3 listener on %s", s3.Name)
	}
	if listening {
		_ = r.RecordEvent(v1.EventTypeNormal, "EnvoyHandoverConfirmed", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
			fmt.Sprintf("wekas3 on %s listens on the LB port beside envoy %s", s3.Name, envoy.Name))
		return nil
	}
	if time.Since(started) >= envoyHandoverTimeout {
		logger.Warn("wekas3 never bound the LB port alongside envoy, draining anyway", "envoy", envoy.Name, "s3", s3.Name)
		_ = r.RecordEvent(v1.EventTypeWarning, "EnvoyHandoverTimedOut", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
			fmt.Sprintf("wekas3 on %s did not bind the LB port beside envoy %s, draining anyway", s3.Name, envoy.Name))
		return nil
	}
	return lifecycle.NewWaitErrorWithDuration(errors.Errorf("waiting for wekas3 on %s to listen on the LB port", s3.Name), 3*time.Second)
}

func (r *wekaClusterReconcilerLoop) s3ListeningOnLbPort(ctx context.Context, s3 *weka.WekaContainer) (bool, error) {
	out, err := r.execIn(ctx, s3, "CheckS3LbListener", 10*time.Second,
		"ss", "-ltnp", fmt.Sprintf("sport = :%d", r.cluster.Status.Ports.LbPort))
	if err != nil {
		return false, err
	}
	return processListening(out, "wekas3"), nil
}

func (r *wekaClusterReconcilerLoop) retireEnvoy(ctx context.Context, envoy *weka.WekaContainer) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	if envoy.IsMarkedForDeletion() {
		return lifecycle.NewWaitError(errors.Errorf("envoy %s is being deleted", envoy.Name))
	}

	graceful := envoy.Annotations[consts.AnnotationEnvoyFastDrain] == fastDrainImmediate
	started, firstPass, err := r.annotateStart(ctx, envoy, consts.AnnotationEnvoyDrainStarted)
	if err != nil {
		return err
	}

	state, drainErr := r.drainEnvoy(ctx, envoy, firstPass, graceful)
	if drainErr != nil || !state.done() {
		if time.Since(started) < envoyDrainTimeout {
			if drainErr != nil {
				return lifecycle.NewWaitErrorWithDuration(errors.Wrapf(drainErr, "draining envoy %s", envoy.Name), 5*time.Second)
			}
			return lifecycle.NewWaitErrorWithDuration(errors.Errorf("envoy %s still draining: %s", envoy.Name, state), 2*time.Second)
		}
		logger.Error(drainErr, "Envoy drain timed out, deleting it anyway", "envoy", envoy.Name, "state", state.String())
	}

	_ = r.RecordEvent(v1.EventTypeNormal, "EnvoyRetired", consts.ActionUpgrade, //nolint:errcheck // event recording errors are intentionally ignored
		fmt.Sprintf("Deleting envoy %s: its S3 container runs the forwarder-capable image (graceful drain: %t)", envoy.Name, graceful))
	if err := r.getClient().Delete(ctx, envoy); err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrapf(err, "failed to delete envoy %s", envoy.Name)
	}
	return lifecycle.NewWaitError(errors.Errorf("envoy %s deleted, waiting for its forwarder", envoy.Name))
}

// annotateStart returns the time recorded under key on the envoy, recording now on the first call.
func (r *wekaClusterReconcilerLoop) annotateStart(ctx context.Context, envoy *weka.WekaContainer, key string) (started time.Time, first bool, err error) {
	if raw, ok := envoy.Annotations[key]; ok {
		started, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, false, errors.Wrapf(err, "invalid %s annotation on %s", key, envoy.Name)
		}
		return started, false, nil
	}
	now := time.Now().UTC()
	if err := r.setEnvoyAnnotation(ctx, envoy, key, now.Format(time.RFC3339)); err != nil {
		return time.Time{}, false, err
	}
	return now, true, nil
}

// drainEnvoy stops envoy's listeners on the first call, gracefully only when the immediate drain policy is in
// effect (the shipped policy is a 600s gradual drain), and reports what is still draining. A graceful drain keeps
// accepting connections for drain_time, so in-flight requests alone reaching zero is not enough: a client that
// reconnects in that window is reset when envoy is deleted. Envoy runs on the host network, so its admin API is
// on loopback and ss sees wekas3's socket on the LB port too.
func (r *wekaClusterReconcilerLoop) drainEnvoy(ctx context.Context, envoy *weka.WekaContainer, drain, graceful bool) (envoyDrainState, error) {
	adminURL := r.envoyAdminURL()
	cmd := fmt.Sprintf(`curl -sSf '%s/stats?filter=http.ingress_http.downstream_' && echo %s && ss -ltnp 'sport = :%d'`,
		adminURL, ssOutputSeparator, r.cluster.Status.Ports.LbPort)
	if drain {
		cmd = fmt.Sprintf(`curl -sSf -X POST '%s%s' >/dev/null && `, adminURL, drainListenersPath(graceful)) + cmd
	}

	out, err := r.execIn(ctx, envoy, "DrainEnvoy", 10*time.Second, "bash", "-ce", cmd)
	if err != nil {
		return envoyDrainState{}, err
	}
	return parseEnvoyDrainState(out)
}

// deleteEnvoys removes every envoy at once for an all-at-once upgrade, where S3 is down anyway.
func (r *wekaClusterReconcilerLoop) deleteEnvoys(ctx context.Context, envoys []*weka.WekaContainer) error {
	for _, envoy := range envoys {
		if envoy.IsMarkedForDeletion() {
			continue
		}
		if err := r.getClient().Delete(ctx, envoy); err != nil && !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to delete envoy %s", envoy.Name)
		}
	}
	return nil
}
