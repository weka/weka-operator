package wekacontainer

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func TestDriveCapacityColumn(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		ratio    *weka.DriveTypesRatio
		want     string
	}{
		{
			name:     "tlc only",
			capacity: 17555,
			ratio:    nil,
			want:     "T 17.1TiB",
		},
		{
			name:     "tlc and qlc split",
			capacity: 30000,
			ratio:    &weka.DriveTypesRatio{Tlc: 1, Qlc: 1},
			want:     "T/Q 14.6/14.6 TiB",
		},
		{
			name:     "qlc only",
			capacity: 20480,
			ratio:    &weka.DriveTypesRatio{Tlc: 0, Qlc: 1},
			want:     "Q 20.0TiB",
		},
		{
			name:     "empty",
			capacity: 0,
			ratio:    nil,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &weka.WekaContainer{}
			c.Spec.ContainerCapacity = tt.capacity
			c.Spec.DriveTypesRatio = tt.ratio
			if got := driveCapacityColumn(c); got != tt.want {
				t.Errorf("driveCapacityColumn() = %q, want %q", got, tt.want)
			}
		})
	}
}
