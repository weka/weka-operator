package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) getCachedActiveMounts(ctx context.Context) (*int, error) {
	if r.activeMounts != nil {
		return r.activeMounts, nil
	}

	activeMounts, err := r.getActiveMounts(ctx)
	if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
		// if no weka fs driver found, we can assume that there are no active mounts
		val := 0
		r.activeMounts = &val
		return r.activeMounts, nil
	}
	if err != nil {
		return nil, err
	}
	r.activeMounts = activeMounts
	return r.activeMounts, nil
}

func (r *containerReconcilerLoop) getActiveMounts(ctx context.Context) (*int, error) {
	agentPod, err := r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		return nil, err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		err = errors.Wrap(err, "error getting node agent token")
		return nil, err
	}

	url := "http://" + agentPod.Status.PodIP + ":8090/getActiveMounts"

	resp, err := util.SendGetRequest(ctx, url, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		err = errors.Wrap(err, "error sending getActiveMountsget request")
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, &NoWekaFsDriverFound{}
		}

		err := errors.New("getActiveMounts request failed")
		return nil, err
	}

	var activeMountsResp struct {
		ActiveMounts int `json:"active_mounts"`
	}
	err = json.NewDecoder(resp.Body).Decode(&activeMountsResp)
	if err != nil {
		err = errors.Wrap(err, "error decoding response")
		return nil, err
	}

	return &activeMountsResp.ActiveMounts, nil
}

func (r *containerReconcilerLoop) noActiveMountsRestriction(ctx context.Context) (bool, error) {
	// do not check active mounts for s3 containers
	if r.container.IsS3Container() {
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

	activeMounts, err := r.getCachedActiveMounts(ctx)
	if err != nil {
		return false, err
	}

	if activeMounts != nil && *activeMounts != 0 {
		err := fmt.Errorf("%d mounts are still active", *activeMounts)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute)

		return false, err
	}

	return true, nil
}
