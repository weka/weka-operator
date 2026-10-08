package allocator

import (
	"testing"

	"github.com/weka/weka-operator/internal/consts"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSignedFullDrivesGiB_IgnoresSharedDrivesAnnotation(t *testing.T) {
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		consts.AnnotationWekaFullDrives: `[{"serial":"a","capacity_gib":1000},{"serial":"b","capacity_gib":2000},{"serial":"c","capacity_gib":3000}]`,
		consts.AnnotationBlockedDrives:  `["c"]`,
		consts.AnnotationSharedDrives:   `not json`,
	}}}
	drives, signed, err := SignedFullDrivesGiB(node)
	if err != nil || !signed {
		t.Fatalf("want signed without error, got signed=%v err=%v", signed, err)
	}
	if len(drives) != 2 || drives[0] != 2000 || drives[1] != 1000 {
		t.Errorf("want [2000 1000], got %v", drives)
	}
}
