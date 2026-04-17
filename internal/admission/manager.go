// Package admission owns the validating-webhook plumbing for Weka CRDs:
// cert bootstrap, ValidatingWebhookConfiguration lifecycle, and the
// evaluator that runs rules registered in internal/validation.
package admission

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

//+kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete

// Constants must match the GVK-derived paths controller-runtime registers
// at runtime; TestWekaClusterValidateWebhookPath asserts the match.
const (
	WekaClusterValidateWebhookPath = "/validate-weka-weka-io-v1alpha1-wekacluster"
	WekaClientValidateWebhookPath  = "/validate-weka-weka-io-v1alpha1-wekaclient"

	// SkipAdmissionLabel on a CR excludes it from admission via the VWC's
	// objectSelector — per-object escape hatch for emergencies.
	SkipAdmissionLabel = "weka.io/skip-admission"
)

type WebhookManager struct {
	client    client.Client
	config    config.WebhookConfig
	namespace string
	logger    logr.Logger
}

func NewWebhookManager(c client.Client, cfg config.WebhookConfig, namespace string, logger logr.Logger) *WebhookManager {
	return &WebhookManager{
		client:    c,
		config:    cfg,
		namespace: namespace,
		logger:    logger,
	}
}

func (m *WebhookManager) EnsureCertificates(ctx context.Context) error {
	caBundle, err := m.ensureCertSecret(ctx)
	if err != nil {
		return fmt.Errorf("failed to ensure cert secret: %w", err)
	}

	if err := m.ensureWebhookConfiguration(ctx, caBundle); err != nil {
		return fmt.Errorf("failed to ensure webhook configuration: %w", err)
	}

	return nil
}

// CleanupIfExists deletes the VWC. Startup-only — running this on
// shutdown would create a validation gap during rolling updates.
func (m *WebhookManager) CleanupIfExists(ctx context.Context) {
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: m.config.WebhookName},
	}
	err := retry.OnError(retry.DefaultBackoff,
		func(err error) bool {
			// Retry on anything except NotFound / forbidden / unauthorized.
			return !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err)
		},
		func() error { return m.client.Delete(ctx, vwc) },
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return // nothing to clean up
		}
		m.logger.Error(err, "Failed to delete orphaned ValidatingWebhookConfiguration — "+
			"webhook is disabled but VWC may still be present; manual cleanup required if failurePolicy was Fail",
			"name", m.config.WebhookName)
		return
	}
	m.logger.Info("Deleted orphaned ValidatingWebhookConfiguration (webhook disabled)", "name", m.config.WebhookName)
}

func (m *WebhookManager) generateCertificates() (caCertPEM, tlsCertPEM, tlsKeyPEM []byte, err error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA serial: %w", err)
	}

	now := time.Now()
	// Backdate NotBefore 1h for clock-skew tolerance against the API server.
	notBefore := now.Add(-1 * time.Hour)
	// Cert is trusted via injected caBundle, not public CA — use max validity
	// to avoid scheduled rotation. Rotation triggers only on SAN change or
	// manual Secret delete.
	notAfter := maxCertExpiry
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			CommonName:   "weka-operator-webhook-ca",
			Organization: []string{"weka.io"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate leaf key: %w", err)
	}

	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate leaf serial: %w", err)
	}

	svcName := m.config.ServiceName
	ns := m.namespace
	dnsNames := []string{
		svcName,
		fmt.Sprintf("%s.%s.svc", svcName, ns),
		fmt.Sprintf("%s.%s.svc.cluster.local", svcName, ns),
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s.%s.svc", svcName, ns),
			Organization: []string{"weka.io"},
		},
		DNSNames:    dnsNames,
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create leaf certificate: %w", err)
	}

	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	tlsCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER})

	leafKeyDER := x509.MarshalPKCS1PrivateKey(leafKey)
	tlsKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: leafKeyDER})

	return caCertPEM, tlsCertPEM, tlsKeyPEM, nil
}

func (m *WebhookManager) ensureCertSecret(ctx context.Context) (caBundle []byte, err error) {
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{Namespace: m.namespace, Name: m.config.SecretName}

	getErr := m.client.Get(ctx, secretKey, secret)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("failed to get cert secret: %w", getErr)
	}

	secretExists := getErr == nil
	if secretExists {
		tlsCert, ok1 := secret.Data["tls.crt"]
		tlsKey, ok2 := secret.Data["tls.key"]
		caCert, ok3 := secret.Data["ca.crt"]
		if ok1 && ok2 && ok3 {
			valid, reason := m.isCertValid(tlsCert)
			if valid {
				m.logger.Info("Using existing webhook cert secret", "secret", m.config.SecretName)
				if writeErr := m.writeCertsToDisk(tlsCert, tlsKey); writeErr != nil {
					return nil, writeErr
				}
				return caCert, nil
			}
			m.logger.Info("Cert secret exists but cert is invalid, regenerating", "secret", m.config.SecretName, "reason", reason)
		} else {
			m.logger.Info("Cert secret exists but is missing keys, regenerating", "secret", m.config.SecretName)
		}
	}

	m.logger.Info("Generating new webhook certificates")
	caCertPEM, tlsCertPEM, tlsKeyPEM, genErr := m.generateCertificates()
	if genErr != nil {
		return nil, fmt.Errorf("failed to generate certificates: %w", genErr)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.config.SecretName,
			Namespace: m.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component":  "webhook",
				"app.kubernetes.io/created-by": "weka-operator",
				"app.kubernetes.io/part-of":    "weka-operator",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": tlsCertPEM,
			"tls.key": tlsKeyPEM,
			"ca.crt":  caCertPEM,
		},
	}

	if !secretExists {
		if createErr := m.client.Create(ctx, newSecret); createErr != nil {
			return nil, fmt.Errorf("failed to create cert secret: %w", createErr)
		}
	} else {
		// Re-fetch under RetryOnConflict so a concurrent writer can't fail startup.
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cur := &corev1.Secret{}
			if getErr := m.client.Get(ctx, secretKey, cur); getErr != nil {
				return getErr
			}
			cur.Data = newSecret.Data
			cur.Type = newSecret.Type
			if cur.Labels == nil {
				cur.Labels = map[string]string{}
			}
			for k, v := range newSecret.Labels {
				cur.Labels[k] = v
			}
			return m.client.Update(ctx, cur)
		})
		if updateErr != nil {
			return nil, fmt.Errorf("failed to update cert secret: %w", updateErr)
		}
	}

	m.logger.Info("Stored webhook certificates in secret", "secret", m.config.SecretName)

	if writeErr := m.writeCertsToDisk(tlsCertPEM, tlsKeyPEM); writeErr != nil {
		return nil, writeErr
	}

	return caCertPEM, nil
}

// maxCertExpiry is the maximum x509 GeneralizedTime value (RFC 5280).
var maxCertExpiry = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// isCertValid checks the cert's validity window and SAN coverage. The SAN
// check catches a renamed Service (WEBHOOK_SERVICE_NAME changed via Helm)
// where the stored cert no longer matches. reason is empty when valid.
func (m *WebhookManager) isCertValid(certPEM []byte) (valid bool, reason string) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false, "failed to decode PEM"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, fmt.Sprintf("failed to parse certificate: %v", err)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) {
		return false, fmt.Sprintf("certificate not yet valid (NotBefore %s)", cert.NotBefore)
	}
	if now.After(cert.NotAfter) {
		return false, fmt.Sprintf("certificate expired at %s", cert.NotAfter)
	}
	expectedSAN := fmt.Sprintf("%s.%s.svc", m.config.ServiceName, m.namespace)
	for _, san := range cert.DNSNames {
		if san == expectedSAN {
			return true, ""
		}
	}
	return false, fmt.Sprintf("certificate SANs %v do not include %q", cert.DNSNames, expectedSAN)
}

func (m *WebhookManager) writeCertsToDisk(tlsCert, tlsKey []byte) error {
	if err := os.MkdirAll(m.config.CertDir, 0o700); err != nil {
		return fmt.Errorf("failed to create cert dir %s: %w", m.config.CertDir, err)
	}

	certPath := filepath.Join(m.config.CertDir, "tls.crt")
	keyPath := filepath.Join(m.config.CertDir, "tls.key")

	if err := os.WriteFile(certPath, tlsCert, 0o600); err != nil {
		return fmt.Errorf("failed to write tls.crt: %w", err)
	}
	if err := os.WriteFile(keyPath, tlsKey, 0o600); err != nil {
		return fmt.Errorf("failed to write tls.key: %w", err)
	}

	m.logger.Info("Wrote webhook certificates to disk", "certDir", m.config.CertDir)
	return nil
}

// ensureWebhookConfiguration creates/updates the VWC. Retries 409 to
// tolerate external writers without failing startup.
func (m *WebhookManager) ensureWebhookConfiguration(ctx context.Context, caBundle []byte) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		desired := m.buildVWC(caBundle)
		existing := &admissionregistrationv1.ValidatingWebhookConfiguration{}

		err := m.client.Get(ctx, client.ObjectKey{Name: m.config.WebhookName}, existing)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to get ValidatingWebhookConfiguration: %w", err)
			}
			if createErr := m.client.Create(ctx, desired); createErr != nil {
				if apierrors.IsAlreadyExists(createErr) {
					return createErr // another writer created it first; retry loops back to Get+Update
				}
				return fmt.Errorf("failed to create ValidatingWebhookConfiguration: %w", createErr)
			}
			m.logger.Info("Created ValidatingWebhookConfiguration", "name", m.config.WebhookName)
			return nil
		}

		existing.Webhooks = desired.Webhooks
		if updateErr := m.client.Update(ctx, existing); updateErr != nil {
			return updateErr // RetryOnConflict will retry 409
		}
		m.logger.Info("Updated ValidatingWebhookConfiguration", "name", m.config.WebhookName)
		return nil
	})
}

// buildVWC always uses failurePolicy: Fail. Per-request warn/error
// routing happens inside the evaluator; enableAdmissionControl: false is the
// sole outage escape hatch and deletes the VWC entirely.
func (m *WebhookManager) buildVWC(caBundle []byte) *admissionregistrationv1.ValidatingWebhookConfiguration {
	sideEffects := admissionregistrationv1.SideEffectClassNone
	timeoutSeconds := int32(10)
	clusterPath := WekaClusterValidateWebhookPath
	clientPath := WekaClientValidateWebhookPath
	failurePolicy := admissionregistrationv1.Fail

	skipSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      SkipAdmissionLabel,
			Operator: metav1.LabelSelectorOpDoesNotExist,
		}},
	}

	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: m.config.WebhookName},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{
				Name:                    "validate.wekacluster.weka.io",
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy,
				TimeoutSeconds:          &timeoutSeconds,
				ObjectSelector:          skipSelector,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{
						Namespace: m.namespace,
						Name:      m.config.ServiceName,
						Path:      &clusterPath,
					},
					CABundle: caBundle,
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"weka.weka.io"},
							APIVersions: []string{"v1alpha1"},
							Resources:   []string{"wekaclusters"},
						},
					},
				},
			},
			{
				Name:                    "validate.wekaclient.weka.io",
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy,
				TimeoutSeconds:          &timeoutSeconds,
				ObjectSelector:          skipSelector,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{
						Namespace: m.namespace,
						Name:      m.config.ServiceName,
						Path:      &clientPath,
					},
					CABundle: caBundle,
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
						},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"weka.weka.io"},
							APIVersions: []string{"v1alpha1"},
							Resources:   []string{"wekaclients"},
						},
					},
				},
			},
		},
	}
}
