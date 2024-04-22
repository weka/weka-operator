package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CredentialsService interface {
	ApplyClusterCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error
	EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error
	GetUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error)
}

func NewCredentialsService(client client.Client, scheme *runtime.Scheme, config *rest.Config) CredentialsService {
	return &credentialsService{
		Client:      client,
		Scheme:      scheme,
		ExecService: NewExecService(config),
	}
}

type credentialsService struct {
	Client      client.Client
	Scheme      *runtime.Scheme
	ExecService ExecService
}

func (r *credentialsService) ApplyClusterCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyClusterCredentials", "cluster", cluster.Name)
	defer end()
	logger.SetPhase("APPLYING_CLUSTER_CREDENTIALS")
	if len(containers) == 0 {
		err := errors.New("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, containers[0])
	if err != nil {
		logger.Error(err, "Error creating executor")
		return errors.Wrap(err, "Error creating executor")
	}

	existingUsers := []resources.WekaUsersResponse{}
	cmd := "weka user -J || wekaauthcli user -J"
	stdout, stderr, err := executor.ExecSensitive(ctx, "WekaListUsers", []string{"bash", "-ce", cmd})
	if err != nil {
		return err
	}
	err = json.Unmarshal(stdout.Bytes(), &existingUsers)
	if err != nil {
		return err
	}

	ensureUser := func(secretName string) error {
		if cluster == nil {
			return errors.New("cluster is nil")
		}
		// fetch secret from k8s
		username, password, err := r.GetUsernameAndPassword(ctx, cluster.Namespace, secretName)
		if err != nil {
			return err
		}

		for _, user := range existingUsers {
			if user.Username == string(username) {
				return nil
			}
		}
		// TODO: This still exposes password via Exec, solution might be to mount both secrets and create by script
		cmd := fmt.Sprintf("weka user add %s ClusterAdmin %s", username, password)
		_, stderr, err := executor.ExecSensitive(ctx, "AddClusterAdminUser", []string{"bash", "-ce", cmd})
		if err != nil {
			return errors.Wrapf(err, "Failed to add user: %s", stderr.String())
		}

		return nil
	}

	logger.WithValues("user_name", cluster.GetOperatorClusterUsername()).Info("Ensuring operator user")
	if err := ensureUser(cluster.GetOperatorSecretName()); err != nil {
		logger.Error(err, "Failed to apply operator user credentials")
		return err
	}

	logger.WithValues("user_name", cluster.GetUserClusterUsername()).Info("Ensuring admin user")
	if err := ensureUser(cluster.GetUserSecretName()); err != nil {
		logger.Error(err, "Failed to apply admin user credentials")
		return err
	}

	for _, user := range existingUsers {
		if user.Username == "admin" {
			cmd = "wekaauthcli user delete admin"
			logger.Info("Deleting default admin user")
			_, stderr, err = executor.ExecSensitive(ctx, "DeleteDefaultAdminUser", []string{"bash", "-ce", cmd})
			if err != nil {
				logger.Error(err, "Failed to delete admin user")
				return errors.Wrapf(err, "Failed to delete default admin user: %s", stderr.String())
			}
			return nil
		}
	}
	logger.SetPhase("CLUSTER_CREDENTIALS_APPLIED")
	return nil
}

func (r *credentialsService) EnsureLoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureLoginCredentials")
	defer end()

	// generate random password
	operatorLogin := cluster.NewOperatorLoginSecret()
	userLogin := cluster.NewUserLoginSecret()

	ensureSecret := func(secret *v1.Secret) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureSecret", "secret", secret.Name)
		defer end()

		if secret == nil {
			err := pretty.Errorf("secret is nil")
			logger.Error(err, "secret is nil")
			return err
		}
		if cluster == nil {
			err := pretty.Errorf("cluster is nil")
			logger.Error(err, "cluster is nil")
			return err
		}
		if r.Scheme == nil {
			err := pretty.Errorf("scheme is nil")
			logger.Error(err, "scheme is nil")
			return err
		}

		err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: secret.Name}, secret)
		if err != nil && apierrors.IsNotFound(err) {
			err := ctrl.SetControllerReference(cluster, secret, r.Scheme)
			if err != nil {
				logger.Error(err, "SetControllerReference failed")
				return err
			}

			err = r.Client.Create(ctx, secret)
			if err != nil {
				logger.Error(err, "Create failed")
				return err
			}
		}

		return nil
	}

	if err := ensureSecret(operatorLogin); err != nil {
		logger.Error(err, "ensureSecret operatorLogin")
		return err
	}
	if err := ensureSecret(userLogin); err != nil {
		logger.Error(err, "ensureSecret userLogin")
		return err
	}
	return nil
}

func (r *credentialsService) GetUsernameAndPassword(ctx context.Context, namespace string, secretName string) (string, string, error) {
	secret := &v1.Secret{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		return "", "", err
	}
	username := secret.Data["username"]
	password := secret.Data["password"]
	return string(username), string(password), nil
}
