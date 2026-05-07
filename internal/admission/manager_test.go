package admission

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
)

func newTestWebhookManager() *WebhookManager {
	return &WebhookManager{
		config: config.WebhookConfig{
			ServiceName: "weka-webhook-service",
		},
		namespace: "weka-operator-system",
		logger:    logr.Discard(),
	}
}

// generateTestCert creates a PEM-encoded certificate with the given validity
// window and the DNS SAN that newTestWebhookManager expects. Callers who want a
// mismatched SAN use generateTestCertWithSANs directly.
func generateTestCert(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	return generateTestCertWithSANs(t, notBefore, notAfter, []string{"weka-webhook-service.weka-operator-system.svc"})
}

func generateTestCertWithSANs(t *testing.T, notBefore, notAfter time.Time, dnsNames []string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

func TestIsCertValid(t *testing.T) {
	m := newTestWebhookManager()

	tests := []struct {
		name      string
		certPEM   []byte
		wantValid bool
		wantInMsg string // substring expected in the reason
	}{
		{
			name:      "valid cert with 5 year expiry",
			certPEM:   generateTestCert(t, time.Now().Add(-1*time.Hour), time.Now().Add(5*365*24*time.Hour)),
			wantValid: true,
		},
		{
			name:      "valid cert expiring in 1 hour (no renewal buffer, still valid)",
			certPEM:   generateTestCert(t, time.Now().Add(-24*time.Hour), time.Now().Add(1*time.Hour)),
			wantValid: true,
		},
		{
			name:      "expired cert",
			certPEM:   generateTestCert(t, time.Now().Add(-48*time.Hour), time.Now().Add(-1*time.Hour)),
			wantValid: false,
			wantInMsg: "expired",
		},
		{
			// Cert issued on a future-clocked node (or a clock-skew rollback)
			// — admission TLS would fail until NotBefore passes, so isCertValid
			// must reject it and force a regeneration.
			name:      "cert with NotBefore in the future",
			certPEM:   generateTestCert(t, time.Now().Add(2*time.Hour), time.Now().Add(365*24*time.Hour)),
			wantValid: false,
			wantInMsg: "not yet valid",
		},
		{
			name:      "cert SAN does not match current service name",
			certPEM:   generateTestCertWithSANs(t, time.Now().Add(-1*time.Hour), time.Now().Add(365*24*time.Hour), []string{"other-service.other-ns.svc"}),
			wantValid: false,
			wantInMsg: "SANs",
		},
		{
			name:      "cert with no SANs",
			certPEM:   generateTestCertWithSANs(t, time.Now().Add(-1*time.Hour), time.Now().Add(365*24*time.Hour), nil),
			wantValid: false,
			wantInMsg: "SANs",
		},
		{
			name:      "malformed PEM",
			certPEM:   []byte("not a PEM"),
			wantValid: false,
			wantInMsg: "failed to decode PEM",
		},
		{
			name:      "valid PEM but invalid DER",
			certPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}),
			wantValid: false,
			wantInMsg: "failed to parse certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, reason := m.isCertValid(tt.certPEM)
			if valid != tt.wantValid {
				t.Errorf("isCertValid() valid = %v, want %v (reason: %s)", valid, tt.wantValid, reason)
			}
			if !tt.wantValid && tt.wantInMsg != "" {
				if !strings.Contains(reason, tt.wantInMsg) {
					t.Errorf("isCertValid() reason = %q, want substring %q", reason, tt.wantInMsg)
				}
			}
		})
	}
}

func TestGenerateCertificates(t *testing.T) {
	m := newTestWebhookManager()

	caCertPEM, tlsCertPEM, tlsKeyPEM, err := m.generateCertificates()
	if err != nil {
		t.Fatalf("generateCertificates() error: %v", err)
	}

	// Parse CA cert
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil {
		t.Fatal("failed to decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse CA cert: %v", err)
	}

	// Verify CA properties
	if !caCert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}
	if caCert.Subject.CommonName != "weka-operator-webhook-ca" {
		t.Errorf("CA CN = %q, want %q", caCert.Subject.CommonName, "weka-operator-webhook-ca")
	}

	// Parse leaf cert
	leafBlock, _ := pem.Decode(tlsCertPEM)
	if leafBlock == nil {
		t.Fatal("failed to decode leaf cert PEM")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse leaf cert: %v", err)
	}

	// Verify leaf SANs
	expectedDNS := []string{
		"weka-webhook-service",
		"weka-webhook-service.weka-operator-system.svc",
		"weka-webhook-service.weka-operator-system.svc.cluster.local",
	}
	if len(leafCert.DNSNames) != len(expectedDNS) {
		t.Fatalf("leaf DNSNames = %v, want %v", leafCert.DNSNames, expectedDNS)
	}
	for i, dns := range leafCert.DNSNames {
		if dns != expectedDNS[i] {
			t.Errorf("leaf DNSNames[%d] = %q, want %q", i, dns, expectedDNS[i])
		}
	}

	// Verify leaf is signed by CA
	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:     caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("leaf cert not signed by CA: %v", err)
	}

	// Verify leaf has ServerAuth EKU
	hasServerAuth := false
	for _, eku := range leafCert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			hasServerAuth = true
			break
		}
	}
	if !hasServerAuth {
		t.Error("leaf cert missing ServerAuth ExtKeyUsage")
	}

	// Verify leaf key is parseable
	keyBlock, _ := pem.Decode(tlsKeyPEM)
	if keyBlock == nil {
		t.Fatal("failed to decode leaf key PEM")
	}
	if _, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err != nil {
		t.Errorf("failed to parse leaf private key: %v", err)
	}

	// Verify expiry is set to the x509 GeneralizedTime maximum (year 9999).
	if leafCert.NotAfter.Year() != 9999 {
		t.Errorf("leaf cert NotAfter year = %d, want 9999 (max expiry)", leafCert.NotAfter.Year())
	}
	if caCert.NotAfter.Year() != 9999 {
		t.Errorf("CA cert NotAfter year = %d, want 9999 (max expiry)", caCert.NotAfter.Year())
	}
}

// TestWekaClusterValidateWebhookPath verifies the webhook path constant matches what
// controller-runtime derives from the WekaCluster GVK. Registration goes through
// ctrl.NewWebhookManagedBy in wekacluster.go (no kubebuilder marker — the path is
// derived from the GVK at runtime). If the API group, version, or kind changes, this
// test will fail — reminding you to update the constant in manager.go.
func TestWekaClusterValidateWebhookPath(t *testing.T) {
	gvk := wekav1alpha1.GroupVersion.WithKind("WekaCluster")
	expected := "/validate-" + strings.ReplaceAll(gvk.Group, ".", "-") + "-" +
		gvk.Version + "-" + strings.ToLower(gvk.Kind)

	if WekaClusterValidateWebhookPath != expected {
		t.Errorf("WekaClusterValidateWebhookPath = %q, want %q (derived from GVK %s)",
			WekaClusterValidateWebhookPath, expected, gvk)
	}
}
