/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhookcertgen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CertgenConfig holds configuration for the webhook certificate generation jobs
type CertgenConfig struct {
	Prefix             string
	Namespace          string
	ServiceAccountName string
	Image              string
	ImagePullPolicy    corev1.PullPolicy
	JobTimeout         time.Duration
	CertDir            string // Local directory to write certs for webhook server
}

// CertgenManager handles the creation and monitoring of webhook certificate generation jobs
type CertgenManager struct {
	client client.Client
	config CertgenConfig
	logger logr.Logger
}

// NewCertgenManager creates a new CertgenManager
func NewCertgenManager(c client.Client, config CertgenConfig, logger logr.Logger) *CertgenManager {
	return &CertgenManager{
		client: c,
		config: config,
		logger: logger.WithName("webhookcertgen"),
	}
}

// EnsureWebhookCertificates ensures that webhook certificates exist and are valid.
// It creates the certificate secret if it doesn't exist, and always ensures
// the webhook configuration has the correct CA bundle.
func (m *CertgenManager) EnsureWebhookCertificates(ctx context.Context) error {
	secretName := fmt.Sprintf("%s-webhook-server-cert", m.config.Prefix)

	// Check if the certificate secret already exists
	secret := &corev1.Secret{}
	err := m.client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: m.config.Namespace,
	}, secret)

	secretExists := false
	if err == nil {
		// Secret exists, check if it has valid certificate data (non-empty values)
		cert := secret.Data["tls.crt"]
		key := secret.Data["tls.key"]
		if len(cert) > 0 && len(key) > 0 {
			m.logger.Info("webhook certificate secret already exists", "secret", secretName)
			secretExists = true
		} else {
			m.logger.Info("webhook certificate secret exists but has empty certificate data, regenerating")
		}
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check certificate secret: %w", err)
	}

	// Run the create-secret job only if secret doesn't exist or is invalid
	if !secretExists {
		m.logger.Info("creating certificate secret")
		if err := m.runCreateSecretJob(ctx); err != nil {
			return fmt.Errorf("failed to run create-secret job: %w", err)
		}
	}

	// Always run the patch-webhook job to ensure the webhook configuration
	// has the correct CA bundle (it may have been recreated by ArgoCD/Helm)
	m.logger.Info("patching webhook configuration with CA bundle")
	if err := m.runPatchWebhookJob(ctx); err != nil {
		return fmt.Errorf("failed to run patch-webhook job: %w", err)
	}

	// Write certificates to local filesystem for the webhook server
	if m.config.CertDir != "" {
		if err := m.writeCertsToLocalFS(ctx, secretName); err != nil {
			return fmt.Errorf("failed to write certificates to local filesystem: %w", err)
		}
	}

	m.logger.Info("webhook certificate setup completed successfully")
	return nil
}

// runCreateSecretJob creates and waits for the certgen create-secret job to complete
func (m *CertgenManager) runCreateSecretJob(ctx context.Context) error {
	jobName := fmt.Sprintf("%s-webhook-certgen-create", m.config.Prefix)
	secretName := fmt.Sprintf("%s-webhook-server-cert", m.config.Prefix)
	serviceName := fmt.Sprintf("%s-webhook-service", m.config.Prefix)

	// Delete any existing job first
	if err := m.deleteJobIfExists(ctx, jobName); err != nil {
		return err
	}

	job := m.buildCreateSecretJob(jobName, secretName, serviceName)

	m.logger.Info("creating certgen create-secret job", "job", jobName)
	if err := m.client.Create(ctx, job); err != nil {
		return fmt.Errorf("failed to create job %s: %w", jobName, err)
	}

	return m.waitForJobCompletion(ctx, jobName)
}

// runPatchWebhookJob creates and waits for the certgen patch-webhook job to complete
func (m *CertgenManager) runPatchWebhookJob(ctx context.Context) error {
	jobName := fmt.Sprintf("%s-webhook-certgen-patch", m.config.Prefix)
	secretName := fmt.Sprintf("%s-webhook-server-cert", m.config.Prefix)
	webhookName := fmt.Sprintf("%s-validating-webhook-configuration", m.config.Prefix)

	// Delete any existing job first
	if err := m.deleteJobIfExists(ctx, jobName); err != nil {
		return err
	}

	job := m.buildPatchWebhookJob(jobName, secretName, webhookName)

	m.logger.Info("creating certgen patch-webhook job", "job", jobName)
	if err := m.client.Create(ctx, job); err != nil {
		return fmt.Errorf("failed to create job %s: %w", jobName, err)
	}

	return m.waitForJobCompletion(ctx, jobName)
}

// buildCreateSecretJob builds the Job spec for creating the certificate secret
func (m *CertgenManager) buildCreateSecretJob(jobName, secretName, serviceName string) *batchv1.Job {
	backoffLimit := int32(1)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: m.config.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component":  "webhook-certgen",
				"app.kubernetes.io/created-by": "weka-operator",
				"app.kubernetes.io/part-of":    "weka-operator",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/component":  "webhook-certgen",
						"app.kubernetes.io/created-by": "weka-operator",
						"app.kubernetes.io/part-of":    "weka-operator",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: m.config.ServiceAccountName,
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:            "create",
							Image:           m.config.Image,
							ImagePullPolicy: m.config.ImagePullPolicy,
							Args: []string{
								"create",
								fmt.Sprintf("--host=%s,%s.%s.svc,%s.%s.svc.cluster.local",
									serviceName,
									serviceName, m.config.Namespace,
									serviceName, m.config.Namespace),
								fmt.Sprintf("--namespace=%s", m.config.Namespace),
								fmt.Sprintf("--secret-name=%s", secretName),
								"--cert-name=tls.crt",
								"--key-name=tls.key",
							},
						},
					},
				},
			},
		},
	}
}

// buildPatchWebhookJob builds the Job spec for patching the webhook configuration
func (m *CertgenManager) buildPatchWebhookJob(jobName, secretName, webhookName string) *batchv1.Job {
	backoffLimit := int32(1)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: m.config.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component":  "webhook-certgen",
				"app.kubernetes.io/created-by": "weka-operator",
				"app.kubernetes.io/part-of":    "weka-operator",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/component":  "webhook-certgen",
						"app.kubernetes.io/created-by": "weka-operator",
						"app.kubernetes.io/part-of":    "weka-operator",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: m.config.ServiceAccountName,
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:            "patch",
							Image:           m.config.Image,
							ImagePullPolicy: m.config.ImagePullPolicy,
							Args: []string{
								"patch",
								fmt.Sprintf("--webhook-name=%s", webhookName),
								fmt.Sprintf("--namespace=%s", m.config.Namespace),
								fmt.Sprintf("--secret-name=%s", secretName),
								"--patch-validating=true",
								"--patch-mutating=false",
							},
						},
					},
				},
			},
		},
	}
}

// deleteJobIfExists deletes a job if it exists
func (m *CertgenManager) deleteJobIfExists(ctx context.Context, jobName string) error {
	job := &batchv1.Job{}
	err := m.client.Get(ctx, types.NamespacedName{
		Name:      jobName,
		Namespace: m.config.Namespace,
	}, job)

	if errors.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for existing job: %w", err)
	}

	m.logger.Info("deleting existing certgen job", "job", jobName)

	propagationPolicy := metav1.DeletePropagationBackground
	if err := m.client.Delete(ctx, job, &client.DeleteOptions{
		PropagationPolicy: &propagationPolicy,
	}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete existing job: %w", err)
	}

	// Wait for the job to be deleted
	return wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		err := m.client.Get(ctx, types.NamespacedName{
			Name:      jobName,
			Namespace: m.config.Namespace,
		}, &batchv1.Job{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// waitForJobCompletion waits for a job to complete successfully
func (m *CertgenManager) waitForJobCompletion(ctx context.Context, jobName string) error {
	timeout := m.config.JobTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	m.logger.Info("waiting for job completion", "job", jobName, "timeout", timeout)

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		job := &batchv1.Job{}
		if err := m.client.Get(ctx, types.NamespacedName{
			Name:      jobName,
			Namespace: m.config.Namespace,
		}, job); err != nil {
			return false, fmt.Errorf("failed to get job status: %w", err)
		}

		// Check for successful completion
		if job.Status.Succeeded > 0 {
			m.logger.Info("job completed successfully", "job", jobName)
			return true, nil
		}

		// Check for failure
		if job.Status.Failed > 0 {
			return false, fmt.Errorf("job %s failed", jobName)
		}

		// Still running
		return false, nil
	})
}

// writeCertsToLocalFS reads certificates from the Kubernetes secret and writes them to the local filesystem
func (m *CertgenManager) writeCertsToLocalFS(ctx context.Context, secretName string) error {
	// Read the secret
	secret := &corev1.Secret{}
	if err := m.client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: m.config.Namespace,
	}, secret); err != nil {
		return fmt.Errorf("failed to read certificate secret: %w", err)
	}

	cert := secret.Data["tls.crt"]
	key := secret.Data["tls.key"]

	if len(cert) == 0 || len(key) == 0 {
		return fmt.Errorf("certificate secret is missing tls.crt or tls.key data")
	}

	// Ensure the cert directory exists
	if err := os.MkdirAll(m.config.CertDir, 0755); err != nil {
		return fmt.Errorf("failed to create cert directory: %w", err)
	}

	// Write the certificate
	certPath := filepath.Join(m.config.CertDir, "tls.crt")
	if err := os.WriteFile(certPath, cert, 0644); err != nil {
		return fmt.Errorf("failed to write certificate file: %w", err)
	}

	// Write the key
	keyPath := filepath.Join(m.config.CertDir, "tls.key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return fmt.Errorf("failed to write key file: %w", err)
	}

	m.logger.Info("certificates written to local filesystem", "certDir", m.config.CertDir)
	return nil
}
