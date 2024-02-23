package controllers

import (
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"k8s.io/client-go/tools/record"
)

type ClientRecorder struct {
	Client   *wekav1alpha1.Client
	Recorder record.EventRecorder
}

func NewClientRecorder(c *wekav1alpha1.Client, recorder record.EventRecorder) *ClientRecorder {
	return &ClientRecorder{c, recorder}
}

func (r *ClientRecorder) Event(eventType, reason, message string) {
	r.Recorder.Event(r.Client, eventType, reason, message)
}
