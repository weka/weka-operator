package services

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/services/mocks"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/common"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestApplyClusterCredentials(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	fixtures.mockExecService.EXPECT().GetExecutor(gomock.Any(), gomock.Any()).Return(fixtures.mockExec, nil).AnyTimes()
	fixtures.mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	fixtures.mockCredentialsService.EXPECT().GetUsernameAndPassword(gomock.Any(), gomock.Any(), gomock.Any()).Return("test-username", "test-password", nil).AnyTimes()

	usersResponse := []resources.WekaUsersResponse{
		{
			Username: "test-username",
		},
	}
	usersResponseJson, err := json.Marshal(usersResponse)
	if err != nil {
		t.Errorf("Failed to marshal users response: %v", err)
	}

	stdout := *bytes.NewBuffer(usersResponseJson)
	stderr := bytes.Buffer{}
	fixtures.mockExec.EXPECT().ExecSensitive(gomock.Any(), gomock.Any(), gomock.Any()).Return(stdout, stderr, nil).AnyTimes()

	ctx := context.Background()
	cluster := &wekav1alpha1.WekaCluster{}
	containers := []*wekav1alpha1.WekaContainer{
		{
			Spec:   wekav1alpha1.WekaContainerSpec{},
			Status: wekav1alpha1.WekaContainerStatus{},
		},
	}

	subject := &credentialsService{
		Client:      fixtures.mockClient,
		ExecService: fixtures.mockExecService,
	}

	tests := []struct {
		name       string
		containers []*wekav1alpha1.WekaContainer
		error      error
	}{
		{
			name:       "empty containers",
			containers: []*wekav1alpha1.WekaContainer{},
			error:      common.ArgumentError{},
		},
		{
			name:       "with containers",
			containers: containers,
			error:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := subject.ApplyClusterCredentials(ctx, cluster, tt.containers)
			if err != tt.error && !errors.As(tt.error, &err) {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestEnsureLoginCredentials(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	ctx := context.Background()
	cluster := &wekav1alpha1.WekaCluster{}
	subject := &credentialsService{
		Client:      fixtures.mockClient,
		Scheme:      fixtures.scheme,
		ExecService: fixtures.mockExecService,
	}

	tests := []struct {
		name       string
		setupMocks func(mockClient *mocks.MockClient)
	}{
		{
			name: "secret exists",
			setupMocks: func(mockClient *mocks.MockClient) {
				mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
		},
		{
			name: "secret does not exist",
			setupMocks: func(mockClient *mocks.MockClient) {
				mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(
					apierrors.NewNotFound(schema.GroupResource{}, "test"),
				).Times(2)
				mockClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupMocks(fixtures.mockClient)
			if err := subject.EnsureLoginCredentials(ctx, cluster); err != nil {
				t.Errorf("Expected nil, got %v", err)
			}
		})
	}
}

func TestGetUsernameAndPassword(t *testing.T) {
	fixtures := setup(t)
	defer fixtures.teardown()

	subject := &credentialsService{
		Client: fixtures.mockClient,
	}

	ctx := context.Background()
	namespace := "test-namespace"
	secretName := "test-secret"

	failure := errors.New("error")
	tests := []struct {
		name     string
		username string
		password string
		apiError error
		err      error
	}{
		{
			name:     "success",
			username: "test-username",
			password: "test-password",
			apiError: nil,
			err:      nil,
		},
		{
			name:     "error",
			username: "",
			password: "",
			apiError: failure,
			err:      failure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &v1.Secret{
				Data: map[string][]byte{
					"username": []byte(tt.username),
					"password": []byte(tt.password),
				},
			}

			fixtures.mockClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if tt.apiError != nil {
						return tt.apiError
					}
					secret.DeepCopyInto(obj.(*v1.Secret))
					return nil
				},
			)
			username, password, err := subject.GetUsernameAndPassword(ctx, namespace, secretName)
			if err != tt.err {
				t.Errorf("Expected %v, got %v", tt.err, err)
			}
			if username != tt.username {
				t.Errorf("Expected %v, got %v", tt.username, username)
			}
			if password != tt.password {
				t.Errorf("Expected %v, got %v", tt.password, password)
			}
		})
	}
}
