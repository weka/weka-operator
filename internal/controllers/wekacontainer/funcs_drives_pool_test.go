package wekacontainer

import (
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestAllDrivesSameTypePool(t *testing.T) {
	mixed := []weka.VirtualDrive{{Type: "TLC"}, {Type: "QLC"}}
	single := []weka.VirtualDrive{{Type: "QLC"}, {Type: "QLC"}}
	tests := []struct {
		name  string
		ratio *weka.DriveTypesRatio
		vds   []weka.VirtualDrive
		want  bool
	}{
		{"ratio 1:10", &weka.DriveTypesRatio{Tlc: 1, Qlc: 10}, mixed, false},
		{"ratio 1:0", &weka.DriveTypesRatio{Tlc: 1, Qlc: 0}, mixed, true},
		{"ratio 0:1", &weka.DriveTypesRatio{Tlc: 0, Qlc: 1}, mixed, true},
		{"nil ratio mixed", nil, mixed, false},
		{"nil ratio single type", nil, single, true},
		{"nil ratio no allocations", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allDrivesSameTypePool(tt.ratio, tt.vds); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
