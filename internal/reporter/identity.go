package reporter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

// tokenCacheMargin is how much time before expiry we consider a cached token stale.
const tokenCacheMargin = time.Minute

// IdentityManager loads or creates the operator's RSA keypair + deployment UUID,
// registers with Weka Home, and signs RS256 JWTs for subsequent requests.
type IdentityManager struct {
	client     client.Client
	wh         *config.WekaHome
	namespace  string
	secretName string
	log        logr.Logger

	deploymentID string
	privateKey   *rsa.PrivateKey
	publicKeyPEM []byte
	cachedToken  string
	tokenExpiry  time.Time
}

// NewIdentityManager constructs an IdentityManager. Identity is loaded lazily
// on the first AuthHeader call.
func NewIdentityManager(c client.Client, wh *config.WekaHome, namespace, secretName string, log logr.Logger) *IdentityManager {
	return &IdentityManager{
		client:     c,
		wh:         wh,
		namespace:  namespace,
		secretName: secretName,
		log:        log.WithName("identity-manager"),
	}
}

// AuthHeader ensures the identity is loaded, then returns a cached or
// freshly-signed JWT prefixed with "SRT ".
func (m *IdentityManager) AuthHeader(ctx context.Context) (string, error) {
	if err := m.ensureIdentity(ctx); err != nil {
		return "", err
	}

	if time.Until(m.tokenExpiry) > tokenCacheMargin {
		return "SRT " + m.cachedToken, nil
	}

	token, expiry, err := m.signToken()
	if err != nil {
		return "", err
	}
	m.cachedToken = token
	m.tokenExpiry = expiry
	return "SRT " + token, nil
}

// ensureIdentity loads or creates the identity Secret on first use.
func (m *IdentityManager) ensureIdentity(ctx context.Context) error {
	if m.deploymentID != "" {
		return nil
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: m.namespace, Name: m.secretName}
	err := m.client.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get identity secret: %w", err)
	}

	if err == nil {
		// Secret already exists — load from it.
		return m.loadFromSecret(secret)
	}

	// Secret does not exist — generate a new identity.
	return m.createIdentity(ctx)
}

func (m *IdentityManager) loadFromSecret(secret *corev1.Secret) error {
	depID, ok := secret.Data["deployment_id"]
	if !ok {
		return fmt.Errorf("identity secret %s/%s missing deployment_id", m.namespace, m.secretName)
	}
	privPEM, ok := secret.Data["private_key"]
	if !ok {
		return fmt.Errorf("identity secret %s/%s missing private_key", m.namespace, m.secretName)
	}

	block, _ := pem.Decode(privPEM)
	if block == nil {
		return fmt.Errorf("identity secret %s/%s: failed to decode private_key PEM", m.namespace, m.secretName)
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("identity secret does not contain an RSA private key")
	}

	pubPEM, ok := secret.Data["public_key"]
	if !ok {
		return fmt.Errorf("identity secret %s/%s missing public_key", m.namespace, m.secretName)
	}

	m.deploymentID = string(depID)
	m.privateKey = rsaKey
	m.publicKeyPEM = pubPEM
	m.log.Info("Loaded existing identity", "deployment_id", m.deploymentID)
	return nil
}

func (m *IdentityManager) createIdentity(ctx context.Context) error {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate RSA key: %w", err)
	}

	privDER, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	depID := uuid.New().String()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.secretName,
			Namespace: m.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component":  "reporter",
				"app.kubernetes.io/created-by": "weka-operator",
				"app.kubernetes.io/part-of":    "weka-operator",
			},
		},
		Data: map[string][]byte{
			"deployment_id": []byte(depID),
			"private_key":   privPEM,
			"public_key":    pubPEM,
		},
	}

	if err := m.client.Create(ctx, secret); err != nil {
		return fmt.Errorf("create identity secret: %w", err)
	}

	m.deploymentID = depID
	m.privateKey = privKey
	m.publicKeyPEM = pubPEM
	m.log.Info("Created new identity", "deployment_id", m.deploymentID)
	return nil
}

type registrationBody struct {
	DeploymentID string `json:"deployment_id"`
	PublicKey    string `json:"public_key"`
}

// Register performs deployment registration against Weka Home
// (POST {deployment_id, public_key}, no Authorization header). Both 201 (new) and
// 409 (already registered) count as success. Best-effort: callers log failures
// and continue. httpClient carries the WekaHome TLS config (the reporter passes
// its cached client so the cacert Secret isn't re-read every attempt).
func (m *IdentityManager) Register(ctx context.Context, httpClient *http.Client) error {
	if m.wh.Endpoint == "" {
		m.log.Info("Weka Home endpoint not configured — skipping registration")
		return nil
	}

	if err := m.ensureIdentity(ctx); err != nil {
		return err
	}
	depID := m.deploymentID
	pubPEM := m.publicKeyPEM

	body := registrationBody{
		DeploymentID: depID,
		PublicKey:    string(pubPEM),
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal registration body: %w", err)
	}

	url := strings.TrimRight(m.wh.Endpoint, "/") + registrationPath
	resp, err := util.SendJsonRequest(ctx, url, bodyBytes, util.RequestOptions{
		Client: httpClient,
	})
	if err != nil {
		return fmt.Errorf("registration request: %w", err)
	}
	defer func() {
		//nolint:errcheck // draining response body before close; error is not actionable
		_, _ = io.Copy(io.Discard, resp.Body)
		//nolint:errcheck // best-effort close on a drained response body
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusCreated:
		m.log.Info("Registered with Weka Home", "status", resp.StatusCode, "deployment_id", depID)
		return nil
	case http.StatusConflict:
		m.log.Info("Already registered with Weka Home", "status", resp.StatusCode, "deployment_id", depID)
		return nil
	default:
		return fmt.Errorf("registration returned unexpected status %d", resp.StatusCode)
	}
}

// DeploymentID ensures the identity is loaded and returns the deployment GUID.
// The reporter uses it to template the ingest path. Safe to call before any
// AuthHeader call (it creates/loads the identity Secret on first use).
func (m *IdentityManager) DeploymentID(ctx context.Context) (string, error) {
	if err := m.ensureIdentity(ctx); err != nil {
		return "", err
	}
	return m.deploymentID, nil
}

func (m *IdentityManager) signToken() (string, time.Time, error) {
	now := time.Now()
	expiry := now.Add(10 * time.Minute)
	claims := jwt.MapClaims{
		"iss": m.deploymentID,
		"iat": now.Unix(),
		"exp": expiry.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(m.privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign JWT: %w", err)
	}
	return signed, expiry, nil
}
