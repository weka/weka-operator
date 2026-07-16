package wekacontainer

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// TestIsForceResignDrivesContainer guards the scope of the cordon exemption in ensurePod:
// only the force-resign-drives adhoc-op helper may be created on an unschedulable node.
// A false positive here would let arbitrary pods be scheduled onto cordoned nodes.
func TestIsForceResignDrivesContainer(t *testing.T) {
	tests := []struct {
		name      string
		container *weka.WekaContainer
		want      bool
	}{
		{
			name: "force-resign adhoc-op is exempt",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode:         weka.WekaContainerModeAdhocOp,
				Instructions: &weka.Instructions{Type: weka.InstructionTypeForceResignDrives},
			}},
			want: true,
		},
		{
			name: "adhoc-op with a different instruction is not exempt",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode:         weka.WekaContainerModeAdhocOp,
				Instructions: &weka.Instructions{Type: weka.InstructionTypeEnsureNICs},
			}},
			want: false,
		},
		{
			name: "adhoc-op without instructions is not exempt",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode: weka.WekaContainerModeAdhocOp,
			}},
			want: false,
		},
		{
			name: "force-resign instruction on a non-adhoc-op mode is not exempt",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode:         weka.WekaContainerModeDrive,
				Instructions: &weka.Instructions{Type: weka.InstructionTypeForceResignDrives},
			}},
			want: false,
		},
		{
			name:      "nil container is not exempt",
			container: nil,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isForceResignDrivesContainer(tt.container); got != tt.want {
				t.Fatalf("isForceResignDrivesContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}
