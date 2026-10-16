package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPolicyLoopDurationTillNext(t *testing.T) {
	interval := 5 * time.Minute
	for _, tc := range []struct {
		status  string
		waiting bool
	}{
		{"Done", true},
		{"Failed", false},
		{"Running", false},
		{"", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			p := &weka.WekaPolicy{}
			p.Spec.Payload.Interval = metav1.Duration{Duration: interval}
			p.Status.Status = tc.status
			p.Status.LastRunTime = metav1.Now()

			got := (&policyLoop{Policy: p}).DurationTillNext()
			if tc.waiting && (got <= 0 || got > interval) {
				t.Errorf("DurationTillNext = %v, want within (0, %v]", got, interval)
			}
			if !tc.waiting && got != 0 {
				t.Errorf("DurationTillNext = %v, want 0", got)
			}
		})
	}
}

func TestEnsureNICsBackoffRemaining(t *testing.T) {
	_, logger := instrumentation.CreateLogSpan(context.Background(), "TestEnsureNICsBackoffRemaining")
	defer logger.End()
	interval := 5 * time.Minute
	for _, tc := range []struct {
		name    string
		result  string
		lastRun time.Time
		waiting bool
	}{
		{"err within interval", `{"err":"boom","results":{}}`, time.Now(), true},
		{"err after interval", `{"err":"boom","results":{}}`, time.Now().Add(-2 * interval), false},
		{"no err", `{"results":{}}`, time.Now(), false},
		{"empty result", "", time.Now(), false},
		{"unparsable result", "not json", time.Now(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &weka.WekaPolicy{}
			p.Spec.Payload.Interval = metav1.Duration{Duration: interval}
			p.Status.LastResult = tc.result
			p.Status.LastRunTime = metav1.NewTime(tc.lastRun)

			got := ensureNICsBackoffRemaining(p, logger)
			if tc.waiting && (got <= 0 || got > interval) {
				t.Errorf("remaining = %v, want within (0, %v]", got, interval)
			}
			if !tc.waiting && got > 0 {
				t.Errorf("remaining = %v, want <= 0", got)
			}
		})
	}
}
