package services

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureLoginCredentialsAdminSecretRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := wekav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	adminSecret := func(username, password string) *v1.Secret {
		return &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-admin", Namespace: "ns"},
			Data:       map[string][]byte{"username": []byte(username), "password": []byte(password)},
		}
	}

	for _, tc := range []struct {
		name          string
		ref           string
		secret        *v1.Secret
		wantErr       bool
		wantGenerated bool
	}{
		{name: "valid secret", ref: "my-admin", secret: adminSecret("myadmin", "pw")},
		{name: "missing secret", ref: "my-admin", wantErr: true},
		{name: "reserved admin username", ref: "my-admin", secret: adminSecret("admin", "pw"), wantErr: true},
		{name: "password with NUL", ref: "my-admin", secret: adminSecret("myadmin", "pw\x00cut"), wantErr: true},
		{name: "ref to client secret name", ref: "weka-client-c", secret: &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "weka-client-c", Namespace: "ns"},
			Data:       map[string][]byte{"username": []byte("myadmin"), "password": []byte("pw")},
		}, wantErr: true},
		{name: "ref unset", wantGenerated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &wekav1alpha1.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: "11111111-2222-3333-4444-555555555555"},
			}
			if tc.ref != "" {
				cluster.Spec.AdminCredentialsSecretRef = &v1.LocalObjectReference{Name: tc.ref}
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster)
			if tc.secret != nil {
				builder = builder.WithObjects(tc.secret)
			}
			c := builder.Build()

			err := NewSecretsService(c, scheme, nil).EnsureLoginCredentials(context.Background(), cluster)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}

			getErr := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "weka-cluster-c"}, &v1.Secret{})
			if tc.wantGenerated && getErr != nil {
				t.Fatalf("generated user secret missing: %v", getErr)
			}
			if !tc.wantGenerated && !apierrors.IsNotFound(getErr) {
				t.Fatalf("weka-cluster-c must not be created, got %v", getErr)
			}
		})
	}
}
