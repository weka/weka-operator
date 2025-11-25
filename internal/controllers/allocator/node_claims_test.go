package allocator

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/consts"
)

func TestNewNodeClaims(t *testing.T) {
	claims := NewNodeClaims()

	if claims == nil {
		t.Fatal("NewNodeClaims returned nil")
	}

	if claims.Drives == nil {
		t.Error("Drives map not initialized")
	}

	if claims.Ports == nil {
		t.Error("Ports map not initialized")
	}

	if len(claims.Drives) != 0 {
		t.Error("Drives map should be empty")
	}

	if len(claims.Ports) != 0 {
		t.Error("Ports map should be empty")
	}
}

func TestAddDriveClaim(t *testing.T) {
	claims := NewNodeClaims()
	claimKey := NewClaimKey("cluster1", "default", "container1")

	// Add first drive
	err := claims.AddDriveClaim("drive1", claimKey)
	if err != nil {
		t.Fatalf("Failed to add drive claim: %v", err)
	}

	if len(claims.Drives) != 1 {
		t.Errorf("Expected 1 drive, got %d", len(claims.Drives))
	}

	if claims.Drives["drive1"] != claimKey {
		t.Errorf("Drive claim key mismatch")
	}

	// Add same drive again (should be idempotent)
	err = claims.AddDriveClaim("drive1", claimKey)
	if err != nil {
		t.Errorf("Re-adding same drive claim should not error: %v", err)
	}

	// Try to add drive claimed by another container
	otherKey := NewClaimKey("cluster1", "default", "container2")
	err = claims.AddDriveClaim("drive1", otherKey)
	if err == nil {
		t.Error("Should not allow adding drive claimed by another container")
	}
}

func TestAddPortClaim(t *testing.T) {
	claims := NewNodeClaims()
	claimKey := NewClaimKey("cluster1", "default", "container1")

	// Add port range
	err := claims.AddPortClaim("15000,100", claimKey)
	if err != nil {
		t.Fatalf("Failed to add port claim: %v", err)
	}

	if len(claims.Ports) != 1 {
		t.Errorf("Expected 1 port range, got %d", len(claims.Ports))
	}

	// Add same port range again (idempotent)
	err = claims.AddPortClaim("15000,100", claimKey)
	if err != nil {
		t.Errorf("Re-adding same port claim should not error: %v", err)
	}

	// Try to add port range claimed by another container
	otherKey := NewClaimKey("cluster1", "default", "container2")
	err = claims.AddPortClaim("15000,100", otherKey)
	if err == nil {
		t.Error("Should not allow adding port range claimed by another container")
	}
}

func TestRemoveClaims(t *testing.T) {
	claims := NewNodeClaims()
	claimKey1 := NewClaimKey("cluster1", "default", "container1")
	claimKey2 := NewClaimKey("cluster1", "default", "container2")

	// Add claims for two containers
	claims.AddDriveClaim("drive1", claimKey1)
	claims.AddDriveClaim("drive2", claimKey1)
	claims.AddDriveClaim("drive3", claimKey2)
	claims.AddPortClaim("15000,100", claimKey1)
	claims.AddPortClaim("15100,100", claimKey2)

	// Remove container1's claims
	claims.RemoveClaims(claimKey1)

	// Verify container1's claims are gone
	if _, exists := claims.Drives["drive1"]; exists {
		t.Error("drive1 should be removed")
	}
	if _, exists := claims.Drives["drive2"]; exists {
		t.Error("drive2 should be removed")
	}
	if _, exists := claims.Ports["15000,100"]; exists {
		t.Error("Port range 15000,100 should be removed")
	}

	// Verify container2's claims still exist
	if claims.Drives["drive3"] != claimKey2 {
		t.Error("drive3 should still belong to container2")
	}
	if claims.Ports["15100,100"] != claimKey2 {
		t.Error("Port range 15100,100 should still belong to container2")
	}
}

func TestRemoveDriveClaims(t *testing.T) {
	claims := NewNodeClaims()
	claimKey := NewClaimKey("cluster1", "default", "container1")

	// Add multiple drives
	claims.AddDriveClaim("drive1", claimKey)
	claims.AddDriveClaim("drive2", claimKey)
	claims.AddDriveClaim("drive3", claimKey)

	// Remove specific drives
	claims.RemoveDriveClaims(claimKey, []string{"drive1", "drive2"})

	// Verify specific drives removed
	if _, exists := claims.Drives["drive1"]; exists {
		t.Error("drive1 should be removed")
	}
	if _, exists := claims.Drives["drive2"]; exists {
		t.Error("drive2 should be removed")
	}

	// Verify drive3 still exists
	if claims.Drives["drive3"] != claimKey {
		t.Error("drive3 should still exist")
	}
}

func TestGetClaimedResources(t *testing.T) {
	claims := NewNodeClaims()
	claimKey1 := NewClaimKey("cluster1", "default", "container1")
	claimKey2 := NewClaimKey("cluster1", "default", "container2")

	// Add claims for multiple containers
	claims.AddDriveClaim("drive1", claimKey1)
	claims.AddDriveClaim("drive2", claimKey1)
	claims.AddDriveClaim("drive3", claimKey2)
	claims.AddPortClaim("15000,100", claimKey1)
	claims.AddPortClaim("15100,100", claimKey2)

	// Get container1's drives
	drives := claims.GetClaimedDrives(claimKey1)
	if len(drives) != 2 {
		t.Errorf("Expected 2 drives for container1, got %d", len(drives))
	}

	// Get container1's ports
	ports := claims.GetClaimedPorts(claimKey1)
	if len(ports) != 1 {
		t.Errorf("Expected 1 port range for container1, got %d", len(ports))
	}
}

func TestEquals(t *testing.T) {
	claims1 := NewNodeClaims()
	claims2 := NewNodeClaims()
	claimKey := NewClaimKey("cluster1", "default", "container1")

	// Empty claims should be equal
	if !claims1.Equals(claims2) {
		t.Error("Empty claims should be equal")
	}

	// Add same claims to both
	claims1.AddDriveClaim("drive1", claimKey)
	claims1.AddPortClaim("15000,100", claimKey)
	claims2.AddDriveClaim("drive1", claimKey)
	claims2.AddPortClaim("15000,100", claimKey)

	if !claims1.Equals(claims2) {
		t.Error("Identical claims should be equal")
	}

	// Add different claim to claims2
	claims2.AddDriveClaim("drive2", claimKey)

	if claims1.Equals(claims2) {
		t.Error("Different claims should not be equal")
	}
}

func TestBuildClaimsFromContainers(t *testing.T) {
	containers := []weka.WekaContainer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "container1",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{Name: "cluster1"},
				},
			},
			Status: weka.WekaContainerStatus{
				Allocations: &weka.ContainerAllocations{
					Drives:    []string{"drive1", "drive2"},
					WekaPort:  15000,
					AgentPort: 15300,
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "container2",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{Name: "cluster1"},
				},
			},
			Status: weka.WekaContainerStatus{
				Allocations: &weka.ContainerAllocations{
					Drives:    []string{"drive3"},
					WekaPort:  15100,
					AgentPort: 15301,
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "container3",
				Namespace: "default",
			},
			Status: weka.WekaContainerStatus{
				// No allocations
			},
		},
	}

	claims := BuildClaimsFromContainers(containers)

	// Check drives
	if len(claims.Drives) != 3 {
		t.Errorf("Expected 3 drive claims, got %d", len(claims.Drives))
	}

	// Check ports (2 weka ranges + 2 agent ports = 4 total)
	if len(claims.Ports) != 4 {
		t.Errorf("Expected 4 port claims, got %d", len(claims.Ports))
	}

	// Verify specific claims
	claimKey1 := NewClaimKey("cluster1", "default", "container1")
	if claims.Drives["drive1"] != claimKey1 {
		t.Error("drive1 should belong to container1")
	}
	if claims.Drives["drive2"] != claimKey1 {
		t.Error("drive2 should belong to container1")
	}

	// Verify port ranges
	expectedWekaRange1 := "15000,100"
	if claims.Ports[expectedWekaRange1] != claimKey1 {
		t.Errorf("Weka port range %s should belong to container1", expectedWekaRange1)
	}

	expectedAgentPort1 := "15300,1"
	if claims.Ports[expectedAgentPort1] != claimKey1 {
		t.Errorf("Agent port %s should belong to container1", expectedAgentPort1)
	}
}

func TestParseNodeClaims(t *testing.T) {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Annotations: map[string]string{
				consts.AnnotationDriveClaims: `{"drive1":"cluster1:default:container1","drive2":"cluster1:default:container1"}`,
				consts.AnnotationPortClaims:  `{"15000,100":"cluster1:default:container1"}`,
			},
		},
	}

	claims, err := ParseNodeClaims(node)
	if err != nil {
		t.Fatalf("Failed to parse node claims: %v", err)
	}

	if len(claims.Drives) != 2 {
		t.Errorf("Expected 2 drives, got %d", len(claims.Drives))
	}

	if len(claims.Ports) != 1 {
		t.Errorf("Expected 1 port range, got %d", len(claims.Ports))
	}

	claimKey := NewClaimKey("cluster1", "default", "container1")
	if claims.Drives["drive1"] != claimKey {
		t.Error("drive1 claim key mismatch")
	}
	if claims.Ports["15000,100"] != claimKey {
		t.Error("Port range claim key mismatch")
	}
}

func TestSaveToNode(t *testing.T) {
	scheme := runtime.NewScheme()
	v1.AddToScheme(scheme)

	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node).
		Build()

	claims := NewNodeClaims()
	claimKey := NewClaimKey("cluster1", "default", "container1")
	claims.AddDriveClaim("drive1", claimKey)
	claims.AddPortClaim("15000,100", claimKey)

	err := claims.SaveToNode(context.Background(), fakeClient, node)
	if err != nil {
		t.Fatalf("Failed to save claims to node: %v", err)
	}

	// Verify annotations were added
	if node.Annotations == nil {
		t.Fatal("Node annotations not set")
	}

	if _, ok := node.Annotations[consts.AnnotationDriveClaims]; !ok {
		t.Error("Drive claims annotation not set")
	}

	if _, ok := node.Annotations[consts.AnnotationPortClaims]; !ok {
		t.Error("Port claims annotation not set")
	}

	// Parse back and verify
	parsedClaims, err := ParseNodeClaims(node)
	if err != nil {
		t.Fatalf("Failed to parse saved claims: %v", err)
	}

	if !claims.Equals(parsedClaims) {
		t.Error("Parsed claims don't match original claims")
	}
}

func TestClaimKeyGeneration(t *testing.T) {
	key := NewClaimKey("cluster1", "default", "container1")
	expected := ClaimKey("cluster1:default:container1")

	if key != expected {
		t.Errorf("Expected %s, got %s", expected, key)
	}
}

func TestClaimKeyFromContainer(t *testing.T) {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "container1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{Name: "cluster1"},
			},
		},
	}

	key := ClaimKeyFromContainer(container)
	expected := ClaimKey("cluster1:default:container1")

	if key != expected {
		t.Errorf("Expected %s, got %s", expected, key)
	}

	// Test container with no owner
	containerNoOwner := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "container2",
			Namespace: "default",
		},
	}

	keyNoOwner := ClaimKeyFromContainer(containerNoOwner)
	expectedNoOwner := ClaimKey(":default:container2")

	if keyNoOwner != expectedNoOwner {
		t.Errorf("Expected %s, got %s", expectedNoOwner, keyNoOwner)
	}
}

func TestParseClaimKey(t *testing.T) {
	// Test valid claim key
	key := ClaimKey("cluster1:default:container1")
	owner, err := ParseClaimKey(key)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if owner.ClusterName != "cluster1" {
		t.Errorf("Expected ClusterName cluster1, got %s", owner.ClusterName)
	}
	if owner.Namespace != "default" {
		t.Errorf("Expected Namespace default, got %s", owner.Namespace)
	}
	if owner.Container != "container1" {
		t.Errorf("Expected Container container1, got %s", owner.Container)
	}

	// Test invalid claim key (too few parts)
	invalidKey := ClaimKey("cluster1:default")
	_, err = ParseClaimKey(invalidKey)
	if err == nil {
		t.Errorf("Expected error for invalid claim key, got nil")
	}

	// Test invalid claim key (too many parts)
	invalidKey2 := ClaimKey("cluster1:default:container1:extra")
	_, err = ParseClaimKey(invalidKey2)
	if err == nil {
		t.Errorf("Expected error for invalid claim key, got nil")
	}

	// Test round-trip: NewClaimKey -> ParseClaimKey
	original := NewClaimKey("test-cluster", "test-namespace", "test-container")
	parsed, err := ParseClaimKey(original)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	roundTrip := NewClaimKey(parsed.ClusterName, parsed.Namespace, parsed.Container)
	if original != roundTrip {
		t.Errorf("Round-trip failed: expected %s, got %s", original, roundTrip)
	}
}
