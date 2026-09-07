package validation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	env "github.com/weka/weka-operator/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// selfSignedCertPEM generates a throwaway self-signed certificate for use as valid Secret data.
func selfSignedCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// selfSignedKeyPEM generates a throwaway private key PEM (no certificate).
func selfSignedKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func fakeClientWithSecrets(t *testing.T, secrets ...*corev1.Secret) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for _, s := range secrets {
		b = b.WithObjects(s)
	}
	return b.Build()
}

func TestClusterWekahomeCacertUsable(t *testing.T) {
	ctx := context.Background()
	v := &clusterWekahomeCacertUsable{}

	newCluster := func(cacertSecret string, wekaHome *weka.WekaHomeConfig) *weka.WekaCluster {
		c := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
		c.Spec.WekaHome = wekaHome
		if wekaHome != nil {
			wekaHome.CacertSecret = cacertSecret
			if wekaHome.Endpoint == "" {
				wekaHome.Endpoint = "https://wekahome.example.com"
			}
		}
		return c
	}

	t.Run("wekaHome unset", func(t *testing.T) {
		c := newCluster("", nil)
		errs := v.Validate(ctx, fakeClientWithSecrets(t), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	t.Run("cacertSecret empty", func(t *testing.T) {
		c := newCluster("", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	// Content the check would otherwise reject, so a skip is the only way these pass.
	unusable := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "combined-tls", Namespace: "ns"},
			Data:       map[string][]byte{"tls.pem": append(selfSignedCertPEM(t), selfSignedKeyPEM(t)...)},
		}
	}

	t.Run("unusable secret is flagged when nothing skips", func(t *testing.T) {
		c := newCluster("combined-tls", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, unusable()), c)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %v", errs)
		}
	})

	t.Run("insecure TLS skips the check", func(t *testing.T) {
		c := newCluster("combined-tls", &weka.WekaHomeConfig{AllowInsecure: true})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, unusable()), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	t.Run("weka home disabled skips the check", func(t *testing.T) {
		c := newCluster("combined-tls", &weka.WekaHomeConfig{})
		c.Spec.WekaHome.Endpoint = ""
		errs := v.Validate(ctx, fakeClientWithSecrets(t, unusable()), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	// A missing Secret is routinely transient and self-heals, so it is not flagged here.
	t.Run("secret missing", func(t *testing.T) {
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	t.Run("secret with valid PEM certificate", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": selfSignedCertPEM(t)},
		}
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	t.Run("secret with empty data", func(t *testing.T) {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"}}
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %v", errs)
		}
	})

	t.Run("secret with non-PEM junk", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": []byte("not a certificate")},
		}
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %v", errs)
		}
	})

	// The shape the x509 parser alone would wave through: AppendCertsFromPEM skips the key block
	// and reports success on the certificate, while weka_runtime.py refuses the whole file.
	t.Run("secret with a combined certificate and key in one data key", func(t *testing.T) {
		combined := append(selfSignedCertPEM(t), selfSignedKeyPEM(t)...)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"},
			Data:       map[string][]byte{"tls.pem": combined},
		}
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %v", errs)
		}
	})

	// A kubernetes.io/tls secret keeps them in separate keys, which the runtime does stage.
	t.Run("secret with certificate and key in separate data keys", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"},
			Data: map[string][]byte{
				"tls.crt": selfSignedCertPEM(t),
				"tls.key": selfSignedKeyPEM(t),
			},
		}
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
		if len(errs) != 0 {
			t.Fatalf("expected 0 errors, got %v", errs)
		}
	})

	t.Run("secret with only a private key", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"},
			Data:       map[string][]byte{"tls.key": selfSignedKeyPEM(t)},
		}
		c := newCluster("my-ca", &weka.WekaHomeConfig{})
		errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %v", errs)
		}
	})
}

func TestClusterWekahomeCacertUsable_FieldPath(t *testing.T) {
	ctx := context.Background()
	v := &clusterWekahomeCacertUsable{}
	c := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	c.Spec.WekaHome = &weka.WekaHomeConfig{CacertSecret: "my-ca", Endpoint: "https://wekahome.example.com"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "ns"}}

	errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), c)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
	if errs[0].Field != "spec.wekaHome.cacertSecret" {
		t.Errorf("unexpected field path %q", errs[0].Field)
	}
}

func TestClusterWekahomeCacertUsable_WrongType(t *testing.T) {
	v := &clusterWekahomeCacertUsable{}
	if errs := v.Validate(context.Background(), fakeClientWithSecrets(t), &weka.WekaClient{}); errs != nil {
		t.Fatalf("expected nil for non-WekaCluster object, got %v", errs)
	}
}

// The operator-wide default is what the pod mounts when the cluster sets nothing, and it is not
// guaranteed to exist in the namespace the cluster is created in.
func TestClusterWekahomeCacertUsable_OperatorWideDefault(t *testing.T) {
	ctx := context.Background()
	v := &clusterWekahomeCacertUsable{}
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}

	savedSecret, savedEndpoint := env.Config.WekaHome.CacertSecret, env.Config.WekaHome.Endpoint
	defer func() {
		env.Config.WekaHome.CacertSecret = savedSecret
		env.Config.WekaHome.Endpoint = savedEndpoint
	}()
	env.Config.WekaHome.CacertSecret = "corp-ca"
	env.Config.WekaHome.Endpoint = "https://wekahome.example.com"

	// A missing secret is routinely transient and self-heals, so it is not flagged here.
	if errs := v.Validate(ctx, fakeClientWithSecrets(t), cluster); len(errs) != 0 {
		t.Fatalf("expected 0 errors for a missing secret, got %v", errs)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "corp-ca", Namespace: "ns"},
		Data:       map[string][]byte{"ca.crt": selfSignedCertPEM(t)},
	}
	if errs := v.Validate(ctx, fakeClientWithSecrets(t, secret), cluster); len(errs) != 0 {
		t.Fatalf("expected 0 errors once the default secret exists, got %v", errs)
	}
}
