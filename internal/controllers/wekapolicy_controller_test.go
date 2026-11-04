package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/controllers/operations"
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

func TestEnsureNICsRetryAfter(t *testing.T) {
	_, logger := instrumentation.CreateLogSpan(context.Background(), "TestEnsureNICsRetryAfter")
	defer logger.End()
	interval := 5 * time.Minute
	for _, tc := range []struct {
		name     string
		result   string
		lastRun  time.Time
		interval time.Duration
		waiting  bool
	}{
		{"err within interval", `{"err":"boom","results":{}}`, time.Now(), interval, true},
		{"err after interval", `{"err":"boom","results":{}}`, time.Now().Add(-2 * interval), interval, false},
		{"no err", `{"results":{}}`, time.Now(), interval, false},
		{"empty result", "", time.Now(), interval, false},
		{"unparsable result", "not json", time.Now(), interval, false},
		{"err with zero interval waits the operation minimum", `{"err":"boom","results":{}}`, time.Now(), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &weka.WekaPolicy{}
			p.Spec.Payload.Interval = metav1.Duration{Duration: tc.interval}
			p.Status.LastResult = tc.result
			p.Status.LastRunTime = metav1.NewTime(tc.lastRun)

			got := time.Until(ensureNICsRetryAfter(p, logger))
			want := max(tc.interval, operations.EnsureNICsWaitDuration)
			if tc.waiting && (got <= 0 || got > want) {
				t.Errorf("remaining = %v, want within (0, %v]", got, want)
			}
			if !tc.waiting && got > 0 {
				t.Errorf("remaining = %v, want <= 0", got)
			}
		})
	}
}
