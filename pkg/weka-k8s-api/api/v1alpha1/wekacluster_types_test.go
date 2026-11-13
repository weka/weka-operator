package v1alpha1_test

import (
	"testing"

	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestNetworkSelector_Equal_PCIDevices(t *testing.T) {
	mlx := v1alpha1.PCIDevices{VendorId: "15b3", DeviceId: "1021"}
	mlxOtherDevice := v1alpha1.PCIDevices{VendorId: "15b3", DeviceId: "1023"}

	tests := []struct {
		name string
		a, b []v1alpha1.PCIDevices
		want bool
	}{
		{name: "equal", a: []v1alpha1.PCIDevices{mlx}, b: []v1alpha1.PCIDevices{mlx}, want: true},
		{name: "different deviceId", a: []v1alpha1.PCIDevices{mlx}, b: []v1alpha1.PCIDevices{mlxOtherDevice}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &v1alpha1.NetworkSelector{PCIDevices: tt.a}
			b := &v1alpha1.NetworkSelector{PCIDevices: tt.b}
			if got := a.Equal(b); got != tt.want {
				t.Errorf("Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}
