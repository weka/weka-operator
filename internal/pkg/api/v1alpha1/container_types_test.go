package v1alpha1

import (
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestSupportsEnsureDriversCondition(t *testing.T) {
	container := &WekaContainer{
		Spec: WekaContainerSpec{
			Mode: "",
		},
	}

	tests := []struct {
		mode     string
		expected bool
	}{
		{"", true},
		{WekaContainerModeDist, false},
		{WekaContainerModeDrive, true},
		{WekaContainerModeDriversLoader, false},
		{WekaContainerModeCompute, true},
		{WekaContainerModeClient, true},
	}

	for _, test := range tests {
		container.Spec.Mode = test.mode
		actual := container.SupportsEnsureDriversCondition()
		if actual != test.expected {
			t.Errorf("SupportsEnsureDriversCondition() - mode: %v, expected: %v, actual: %v", test.mode, test.expected, container.SupportsEnsureDriversCondition())
		}
	}
}

func TestInitEnsureDriversCondition(t *testing.T) {
	container := &WekaContainer{
		Spec: WekaContainerSpec{
			Mode: "",
		},
		Status: WekaContainerStatus{
			Conditions: []v1.Condition{},
		},
	}

	tests := []struct {
		mode     string
		expected metav1.ConditionStatus
	}{
		{"", metav1.ConditionFalse},
		{WekaContainerModeDist, metav1.ConditionFalse},
		{WekaContainerModeDrive, metav1.ConditionFalse},
		{WekaContainerModeDriversLoader, metav1.ConditionFalse},
		{WekaContainerModeCompute, metav1.ConditionFalse},
		{WekaContainerModeClient, metav1.ConditionFalse},
	}

	for _, test := range tests {
		container.Spec.Mode = test.mode
		container.InitEnsureDriversCondition()
		actual := meta.FindStatusCondition(container.Status.Conditions, condition.CondEnsureDrivers)
		if actual.Status != test.expected {
			t.Errorf("SupportsEnsureDriversCondition() - mode: %v, expected: %v, actual: %v",
				test.mode, test.expected, actual)
		}

		if actual.Message != "Init" {
			t.Errorf("SupportsEnsureDriversCondition() - mode: %v, expected: %v, actual: %v",
				test.mode, "Init", actual.Message)
		}
	}
}
