package v1alpha1

import (
	"testing"

	"github.com/weka/weka-operator/internal/controllers/condition"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCpuPolicyIsValid(t *testing.T) {
	tests := []struct {
		policy   CpuPolicy
		expected bool
	}{
		{CpuPolicyAuto, true},
		{CpuPolicyShared, true},
		{CpuPolicyDedicated, true},
		{CpuPolicyDedicatedHT, true},
		{CpuPolicyManual, true},
		{CpuPolicy("invalid"), false},
	}

	for _, test := range tests {
		actual := test.policy.IsValid()
		if actual != test.expected {
			t.Errorf("IsValid() - policy: %v, expected: %v, actual: %v", test.policy, test.expected, actual)
		}
	}
}

func TestDriversReady(t *testing.T) {
	container := &WekaContainer{
		Status: WekaContainerStatus{
			Conditions: []v1.Condition{
				{
					Type:   condition.CondEnsureDrivers,
					Status: v1.ConditionUnknown,
				},
			},
		},
	}

	tests := []struct {
		status   v1.ConditionStatus
		expected bool
	}{
		{v1.ConditionUnknown, false},
		{v1.ConditionTrue, true},
		{v1.ConditionFalse, false},
	}

	for _, test := range tests {
		container.Status.Conditions[0].Status = test.status
		actual := container.DriversReady()
		if actual != test.expected {
			t.Errorf("DriversReady() - status: %v, expected: %v, actual: %v", test.status, test.expected, container.DriversReady())
		}
	}
}

func TestIsDistMode(t *testing.T) {
	container := &WekaContainer{
		Spec: WekaContainerSpec{
			Mode: "",
		},
	}

	tests := []struct {
		mode     string
		expected bool
	}{
		{"", false},
		{WekaContainerModeDist, true},
		{WekaContainerModeDrive, false},
		{WekaContainerModeDriversLoader, false},
		{WekaContainerModeCompute, false},
		{WekaContainerModeClient, false},
	}

	for _, test := range tests {
		container.Spec.Mode = test.mode
		actual := container.IsDistMode()
		if actual != test.expected {
			t.Errorf("IsDistMode() - mode: %v, expected: %v, actual: %v", test.mode, test.expected, container.IsDistMode())
		}
	}
}

func TestIsDriversLoaderMode(t *testing.T) {
	container := &WekaContainer{
		Spec: WekaContainerSpec{
			Mode: "",
		},
	}

	tests := []struct {
		mode     string
		expected bool
	}{
		{"", false},
		{WekaContainerModeDist, false},
		{WekaContainerModeDrive, false},
		{WekaContainerModeDriversLoader, true},
		{WekaContainerModeCompute, false},
		{WekaContainerModeClient, false},
	}

	for _, test := range tests {
		container.Spec.Mode = test.mode
		actual := container.IsDriversLoaderMode()
		if actual != test.expected {
			t.Errorf("IsDriversLoaderMode() - mode: %v, expected: %v, actual: %v", test.mode, test.expected, container.IsDriversLoaderMode())
		}
	}
}
