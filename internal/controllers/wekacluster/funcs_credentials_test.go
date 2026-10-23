package wekacluster

import (
	"context"
	"fmt"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1/condition"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services"
)

func TestSyncAdminCredentials(t *testing.T) {
	ctx := context.Background()
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.GetUserSecretName(), Namespace: "ns", UID: "secret-uid"},
		Data:       map[string][]byte{"username": []byte("myadmin"), "password": []byte("pw")},
	}
	c := newFakeClient(t, cluster, secret)
	if err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
		t.Fatal(err)
	}
	version := string(secret.UID) + "/" + secret.ResourceVersion

	for _, tc := range []struct {
		name    string
		message string
		wantErr string
	}{
		{name: "no condition seeds without sync"},
		{name: "up to date", message: version},
		{name: "stale", message: "secret-uid/0", wantErr: "No active container found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loopCluster := &weka.WekaCluster{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(cluster), loopCluster); err != nil {
				t.Fatal(err)
			}
			loopCluster.Status.Conditions = nil
			if tc.message != "" {
				meta.SetStatusCondition(&loopCluster.Status.Conditions, metav1.Condition{
					Type: condition.CondAdminCredentialsSynced, Status: metav1.ConditionTrue, Reason: "Synced", Message: tc.message,
				})
			}
			loop := &wekaClusterReconcilerLoop{
				Manager:        fakeManagerWithClient{c: c},
				cluster:        loopCluster,
				SecretsService: services.NewSecretsService(c, c.Scheme(), nil),
			}
			// no containers: a sync attempt fails on container selection, before reaching WEKA
			err := loop.SyncAdminCredentials(ctx)
			if got := fmt.Sprint(err); (err != nil || tc.wantErr != "") && got != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := getConditionConfigHash(loopCluster.Status.Conditions, condition.CondAdminCredentialsSynced); tc.wantErr == "" && got != version {
				t.Fatalf("condition message = %q, want %q", got, version)
			}
			if tc.wantErr != "" {
				cond := meta.FindStatusCondition(loopCluster.Status.Conditions, condition.CondAdminCredentialsSynced)
				if cond == nil || cond.Status != metav1.ConditionFalse || cond.Message != tc.wantErr {
					t.Fatalf("condition = %+v, want False/%q", cond, tc.wantErr)
				}
			}
		})
	}
}

func TestSyncAdminCredentialsFailureWithoutCondition(t *testing.T) {
	ctx := context.Background()
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec:       weka.WekaClusterSpec{AdminCredentialsSecretRef: &corev1.LocalObjectReference{Name: "missing"}},
	}
	c := newFakeClient(t, cluster)
	loop := &wekaClusterReconcilerLoop{
		Manager:        fakeManagerWithClient{c: c},
		cluster:        cluster,
		SecretsService: services.NewSecretsService(c, c.Scheme(), nil),
	}
	// a pre-existing cluster must stay without the condition, so recovery only records the version
	if err := loop.SyncAdminCredentials(ctx); err == nil {
		t.Fatal("expected an error for a missing secret")
	}
	if cond := meta.FindStatusCondition(cluster.Status.Conditions, condition.CondAdminCredentialsSynced); cond != nil {
		t.Fatalf("condition = %+v, want none", cond)
	}
}
