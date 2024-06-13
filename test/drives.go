package test

import (
	"context"
	"encoding/json"
	"testing"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Drives struct {
	ClusterTest
}

func (d *Drives) Run(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		t.Run("Sign Drives", d.SignDrives(ctx))
		t.Run("Discover Drives", d.DiscoverDrives(ctx))
	}
}

type SignDriveInfo struct {
	Err    interface{} `json:"err"` // Can use interface{} if err could be any type (e.g., null or a string)
	Drives []string    `json:"drives"`
}

type SignDriveResults struct {
	Results map[string]SignDriveInfo `json:"results"`
}

// Drive represents the structure of each drive object in the JSON.
type Drive struct {
	SerialID    string `json:"serial_id"`
	WekaGUID    string `json:"weka_guid"`
	BlockDevice string `json:"block_device"`
	Partition   string `json:"partition"`
}

// DriveInfo represents the structure for each IP address entry.
type DriveInfo struct {
	Err    interface{} `json:"err"`
	Drives []Drive     `json:"drives"`
}

type DiscoverDriveResults struct {
	Results map[string]DriveInfo `json:"results"`
}

func (d *Drives) SignDrives(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, _, done := instrumentation.GetLogSpan(ctx, "SignDrives")
		defer done()

		signOp := &wekav1alpha1.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sign-drives",
				Namespace: d.Cluster.OperatorNamespace,
			},
			Spec: wekav1alpha1.WekaManualOperationSpec{
				Action:          "sign-drives",
				Image:           d.Image,
				ImagePullSecret: "quay-io-robot-secret",
				Payload: wekav1alpha1.ManualOperatorPayload{
					SignDrives: &wekav1alpha1.SignDrivesPayload{
						Type:         "aws-all",
						NodeSelector: map[string]string{},
					},
				},
			},
		}

		err := d.Create(ctx, signOp)
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatalf("failed to create sign drives operation: %v", err)
		}

		waitFor(ctx, func(ctx context.Context) bool {
			key := client.ObjectKeyFromObject(signOp)
			err := d.Get(ctx, key, signOp)
			return err == nil && signOp.Status.Result != ""
		})

		jsonData := signOp.Status.Result
		var result SignDriveResults
		if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
			t.Errorf("failed to unmarshal sign drives result: %v", err)
		}

		if len(result.Results) != 6 {
			t.Errorf("expected 6 results, got %d", len(result.Results))
		}

		for node, driveInfo := range result.Results {
			t.Run(node, func(t *testing.T) {
				if driveInfo.Err != nil {
					t.Errorf("error: %v", driveInfo.Err)
				}

				if len(driveInfo.Drives) == 0 {
					t.Errorf("no drives found")
				}
			})
		}
	}
}

func (d *Drives) DiscoverDrives(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, _, done := instrumentation.GetLogSpan(ctx, "DiscoverDrives")
		defer done()

		discoverOp := &wekav1alpha1.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "discover-drives",
				Namespace: d.Cluster.OperatorNamespace,
			},
			Spec: wekav1alpha1.WekaManualOperationSpec{
				Action:          "discover-drives",
				Image:           d.Image,
				ImagePullSecret: "quay-io-robot-secret",
				Payload: wekav1alpha1.ManualOperatorPayload{
					DiscoverDrives: &wekav1alpha1.DiscoverDrivesPayload{
						NodeSelector: map[string]string{},
					},
				},
			},
		}

		if err := d.Create(ctx, discoverOp); err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				t.Fatalf("failed to create discover drives operation: %v", err)
			}
		}

		waitFor(ctx, func(ctx context.Context) bool {
			key := client.ObjectKeyFromObject(discoverOp)
			err := d.Get(ctx, key, discoverOp)
			return err == nil && discoverOp.Status.Result != ""
		})

		jsonData := discoverOp.Status.Result
		var result DiscoverDriveResults
		err := json.Unmarshal([]byte(jsonData), &result)
		if err != nil {
			t.Errorf("failed to unmarshal sign drives result: %v", err)
		}

		if len(result.Results) != 6 {
			t.Errorf("expected 6 results, got %d", len(result.Results))
		}
	}
}
