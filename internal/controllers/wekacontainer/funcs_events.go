package wekacontainer

import (
	"fmt"
	"time"

	"github.com/weka/go-steps-engine/throttling"
	v1 "k8s.io/api/core/v1"
)

func (r *containerReconcilerLoop) RecordEvent(eventtype, reason, message string) error {
	if r.container == nil {
		return fmt.Errorf("container is not set")
	}
	if eventtype == "" {
		normal := v1.EventTypeNormal
		eventtype = normal
	}

	r.Recorder.Event(r.container, eventtype, reason, message)
	return nil
}

func (r *containerReconcilerLoop) RecordEventThrottled(eventtype, reason, message string, interval time.Duration) error {
	throttler := r.ThrottlingMap.WithPartition("container/" + r.container.Name)

	if !throttler.ShouldRun(eventtype+reason, &throttling.ThrottlingSettings{
		Interval:                    interval,
		DisableRandomPreSetInterval: true,
	}) {
		return nil
	}

	return r.RecordEvent(eventtype, reason, message)
}
