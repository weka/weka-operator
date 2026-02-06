package operations

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

func TestGetAlreadySignedDrives_NewFormat(t *testing.T) {
	entries := []domain.DriveEntry{
		{Serial: "SERIAL1", CapacityGiB: 500},
		{Serial: "SERIAL2", CapacityGiB: 1000},
	}
	data, _ := json.Marshal(entries)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationWekaDrives: string(data),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 2 {
		t.Fatalf("expected 2 drives, got %d", len(drives))
	}
	if drives[0] != "SERIAL1" || drives[1] != "SERIAL2" {
		t.Errorf("unexpected drives: %v", drives)
	}
}

func TestGetAlreadySignedDrives_OldFormat(t *testing.T) {
	serials := []string{"OLD1", "OLD2", "OLD3"}
	data, _ := json.Marshal(serials)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationWekaDrives: string(data),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 3 {
		t.Fatalf("expected 3 drives, got %d", len(drives))
	}
	for i, serial := range serials {
		if drives[i] != serial {
			t.Errorf("drive %d: expected %q, got %q", i, serial, drives[i])
		}
	}
}

func TestGetAlreadySignedDrives_SharedDrives(t *testing.T) {
	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SHARED1", CapacityGiB: 100, Type: "TLC"},
		{PhysicalUUID: "uuid2", Serial: "SHARED2", CapacityGiB: 200, Type: "QLC"},
	}
	data, _ := json.Marshal(sharedDrives)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationSharedDrives: string(data),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 2 {
		t.Fatalf("expected 2 drives, got %d", len(drives))
	}
	if drives[0] != "SHARED1" || drives[1] != "SHARED2" {
		t.Errorf("unexpected drives: %v", drives)
	}
}

func TestGetAlreadySignedDrives_BothAnnotations(t *testing.T) {
	regularEntries := []domain.DriveEntry{
		{Serial: "REG1", CapacityGiB: 500},
	}
	regularData, _ := json.Marshal(regularEntries)

	sharedDrives := []domain.SharedDriveInfo{
		{PhysicalUUID: "uuid1", Serial: "SHARED1", CapacityGiB: 100, Type: "TLC"},
	}
	sharedData, _ := json.Marshal(sharedDrives)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				consts.AnnotationWekaDrives:   string(regularData),
				consts.AnnotationSharedDrives: string(sharedData),
			},
		},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 2 {
		t.Fatalf("expected 2 drives, got %d", len(drives))
	}
}

func TestGetAlreadySignedDrives_NoAnnotations(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{},
	}

	drives := getAlreadySignedDrives(node)
	if len(drives) != 0 {
		t.Fatalf("expected 0 drives, got %d", len(drives))
	}
}
