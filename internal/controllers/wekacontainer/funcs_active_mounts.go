package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/node_agent"
	"github.com/weka/weka-operator/pkg/util"
)

// frontendState is what the wekafs driver reports about a client's frontends.
type frontendState int

const (
	frontendUnknown frontendState = iota
	frontendNotConnected
	frontendConnected
)

// activeMountsInfo is the node-agent's view of a container's driver interface.
type activeMountsInfo struct {
	mounts   int
	frontend frontendState
}

// GetActiveMounts returns the number of active mounts for this container. It honors the SkipActiveMountsCheck
// override, which concerns deleting the container only: the CSI label reads getActiveMountsInfo directly.
func (r *containerReconcilerLoop) GetActiveMounts(ctx context.Context) (*int, error) {
	if r.container.Spec.GetOverrides().SkipActiveMountsCheck {
		_, logger := instrumentation.CreateLogSpan(ctx, "skipActiveMountsCheck")
		defer logger.End()

		logger.Info("SkipActiveMountsCheck override set, assuming no active mounts")
		return new(int), nil
	}

	info, err := r.getActiveMountsInfo(ctx)
	if err != nil {
		return nil, err
	}
	return &info.mounts, nil
}

// getActiveMountsInfo returns the active mounts and the frontend state of this container. Results, including
// failures, are cached for the duration of the reconciliation loop.
func (r *containerReconcilerLoop) getActiveMountsInfo(ctx context.Context) (activeMountsInfo, error) {
	if r.activeMountsInfo != nil {
		return *r.activeMountsInfo, nil
	}
	if r.activeMountsErr != nil {
		return activeMountsInfo{}, r.activeMountsErr
	}

	resp, err := r.fetchActiveMounts(ctx)
	info, err := resolveActiveMounts(resp, err)
	if err != nil {
		r.activeMountsErr = err
		return activeMountsInfo{}, err
	}
	r.activeMountsInfo = &info
	return info, nil
}

// resolveActiveMounts turns a node-agent answer into the mounts and frontend state; failures stay errors.
func resolveActiveMounts(resp *node_agent.ActiveMountsResponse, err error) (activeMountsInfo, error) {
	if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
		// if no weka fs driver found, we can assume that there are no active mounts
		return activeMountsInfo{frontend: frontendNotConnected}, nil
	}
	if err != nil {
		return activeMountsInfo{}, err
	}
	return activeMountsInfo{mounts: resp.ActiveMounts, frontend: frontendStateOf(resp.FrontendConnected)}, nil
}

// frontendStateOf maps the node-agent's optional field; an older node-agent omits it.
func frontendStateOf(reported *bool) frontendState {
	switch {
	case reported == nil:
		return frontendUnknown
	case *reported:
		return frontendConnected
	}
	return frontendNotConnected
}

func (r *containerReconcilerLoop) fetchActiveMounts(ctx context.Context) (*node_agent.ActiveMountsResponse, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "fetchActiveMounts")
	defer logger.End()

	nodeName := r.container.GetNodeAffinity()

	agentPod, err := r.GetNodeAgentPod(ctx, nodeName)

	var nodeAgentPodNotFoundErr *NodeAgentPodNotFound
	if err != nil && errors.As(err, &nodeAgentPodNotFoundErr) {
		// check node not found error as well
		_, getNodeErr := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
		if apierrors.IsNotFound(getNodeErr) {
			// if node is not found, we can assume that there are no active mounts
			logger.Info("Node not found in cluster, assuming no active mounts", "node", nodeName)
			return &node_agent.ActiveMountsResponse{}, nil
		}
		if getNodeErr != nil {
			logger.Error(getNodeErr, "Failed to get node, ignoring", "node", nodeName)
		}
	}
	if err != nil {
		return nil, err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		err = errors.Wrap(err, "error getting node agent token")
		return nil, err
	}

	// Build URL with container_name and container_uuid query parameters
	endpoint := node_agent.URL(agentPod.Status.PodIP, "/getActiveMounts")
	if r.container.Spec.WekaContainerName != "" {
		// container_uuid is the UID of the WekaContainer CR
		query := url.Values{"container_name": {r.container.Spec.WekaContainerName}, "container_uuid": {string(r.container.GetUID())}}
		endpoint += "?" + query.Encode()
	}

	resp, requestErr := util.SendGetRequest(ctx, endpoint, util.RequestOptions{AuthHeader: "Token " + token})
	if requestErr != nil {
		requestErr = errors.Wrap(requestErr, "error sending getActiveMountsget request")
		return nil, requestErr
	}
	defer resp.Body.Close() //nolint:errcheck // error return value intentionally not checked

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, &NoWekaFsDriverFound{}
		}

		reqErr := errors.New("getActiveMounts request failed")
		return nil, reqErr
	}

	var activeMountsResp node_agent.ActiveMountsResponse
	err = json.NewDecoder(resp.Body).Decode(&activeMountsResp)
	if err != nil {
		err = errors.Wrap(err, "error decoding response")
		return nil, err
	}

	return &activeMountsResp, nil
}

func (r *containerReconcilerLoop) noActiveMountsRestriction(ctx context.Context) (bool, error) {
	// do not check active mounts for s3 containers
	if r.container.IsProtocolContainer() {
		return true, nil
	}

	// if container did not join cluster, we can skip active mounts check
	// NOTE: case - pod was stuck in Pending state and wekacontainer CR was deleted afterwards
	// we'd want to allow this container to be recreated by client reconciler
	if r.container.Status.ClusterContainerID == nil {
		return true, nil
	}

	if r.container.Spec.GetOverrides().SkipActiveMountsCheck {
		return true, nil
	}

	activeMounts, err := r.GetActiveMounts(ctx)
	if err != nil {
		return false, err
	}

	if activeMounts != nil && *activeMounts != 0 {
		err := fmt.Errorf("%d mounts are still active", *activeMounts)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", consts.ActionDrainContainer, err.Error(), time.Minute) //nolint:errcheck // error return value intentionally not checked

		return false, err
	}

	return true, nil
}
