//go:generate mockgen -destination=mocks/mock_client.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client Client
//go:generate mockgen -destination=mocks/mock_status.go -package=mocks sigs.k8s.io/controller-runtime/pkg/client StatusWriter
//go:generate mockgen -destination=mocks/mock_exec_service.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services ExecService
//go:generate mockgen -destination=mocks/mock_exec.go -package=mocks github.com/weka/weka-operator/util Exec
//go:generate mockgen -destination=mocks/mock_lifecycle.go -package=mocks github.com/weka/weka-operator/internal/app/manager/controllers ReconciliationPhase
//go:generate mockgen -destination=mocks/mock_weka_cluster_service.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services WekaClusterService
//go:generate mockgen -destination=mocks/mock_allocation_service.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services AllocationService
//go:generate mockgen -destination=mocks/mock_credentials_service.go -package=mocks github.com/weka/weka-operator/internal/app/manager/services CredentialsService
package controllers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/mocks"
	"github.com/weka/weka-operator/internal/app/manager/factories"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlruntime "sigs.k8s.io/controller-runtime"
)

func Cluster() *wekav1alpha1.WekaCluster {
	cluster := &wekav1alpha1.WekaCluster{
		Spec: wekav1alpha1.WekaClusterSpec{
			Size:      1,
			Topology:  "dev_wekabox",
			Template:  "dev",
			CpuPolicy: wekav1alpha1.CpuPolicyAuto,
		},
	}
	return cluster
}

func TestingReconcilerStateMachine(t *testing.T) *reconcilerStateMachine {
	t.Helper()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	if err := wekav1alpha1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Printf("failed to add scheme: %v", err)
	}
	reconciler := &WekaClusterReconciler{
		Scheme:               scheme.Scheme,
		WekaContainerFactory: factories.NewWekaContainerFactory(scheme.Scheme),
	}
	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	cluster := Cluster()
	stateMachine := &reconcilerStateMachine{
		Client:             reconciler.Client,
		WekaClusterService: wekaClusterService,
		Cluster:            cluster,
	}
	return stateMachine
}

func TestRunLoop(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctrl := gomock.NewController(t)

	tests := []struct {
		name      string
		phase     *mocks.MockReconciliationPhase
		expected  error
		requeue   ctrlruntime.Result
		completed bool
		ready     bool
	}{
		{
			name:      "completed phase",
			phase:     mocks.NewMockReconciliationPhase(ctrl),
			expected:  nil,
			requeue:   ctrlruntime.Result{},
			completed: true,
		},
		{
			name:      "not ready phase",
			phase:     mocks.NewMockReconciliationPhase(ctrl),
			expected:  nil,
			requeue:   ctrlruntime.Result{Requeue: true},
			completed: false,
			ready:     false,
		},
		{
			name:      "error phase",
			phase:     mocks.NewMockReconciliationPhase(ctrl),
			expected:  fmt.Errorf("error"),
			requeue:   ctrlruntime.Result{},
			completed: false,
			ready:     true,
		},
		{
			name:      "requeue phase",
			phase:     mocks.NewMockReconciliationPhase(ctrl),
			expected:  nil,
			requeue:   ctrlruntime.Result{Requeue: true},
			completed: false,
			ready:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.phase.EXPECT().String().Return(test.name)
			test.phase.EXPECT().Handle(gomock.Any()).Return(test.requeue, test.expected).AnyTimes()

			phases := []ReconciliationPhase{test.phase}
			stateMachine := TestingReconcilerStateMachine(t)
			stateMachine.PhasesList = &phases
			result, err := stateMachine.RunLoop(context.Background())
			// if err != test.expected {
			if !errors.Is(err, test.expected) {
				t.Errorf("expected error to be %v, was %v", test.expected, err)
			}
			if result.Requeue != test.requeue.Requeue {
				t.Errorf("%s: expected result to requeue %v, was %v", test.name, test.requeue, result.Requeue)
			}
		})
	}
}

func TestPhases(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)
	phases := stateMachine.Phases()
	if len(phases) == 0 {
		t.Errorf("expected phases to be non-empty")
	}
}

func TestSecretsNotCreatedCondition(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	stateMachine.WekaClusterService = wekaClusterService
	wekaClusterService.EXPECT().EnsureClusterContainerIds(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	credentialsService := mocks.NewMockCredentialsService(ctrl)
	stateMachine.CredentialsService = credentialsService
	credentialsService.EXPECT().EnsureLoginCredentials(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Status().Return(statusWriter).AnyTimes()
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	condition := &SecretsNotCreatedCondition{ReconcilerStateMachine: stateMachine}

	// TODO: Test non-happy path
	result, err := condition.Handle(context.Background())
	if err != nil {
		t.Errorf("expected error to be nil")
	}
	if result.Requeue {
		t.Errorf("expected result to not requeue")
	}
}

func TestPodsNotCreatedCondition(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Status().Return(statusWriter).AnyTimes()
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	containers := []*wekav1alpha1.WekaContainer{}
	wekaClusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(containers, nil).AnyTimes()
	stateMachine.WekaClusterService = wekaClusterService

	podsNotCreatedCondition := &PodsNotCreatedCondition{ReconcilerStateMachine: stateMachine}

	tests := []struct {
		name           string
		readyCondition metav1.ConditionStatus
		postCondition  metav1.ConditionStatus
		podsReady      bool
	}{
		{
			name:           "Pods Became Ready",
			readyCondition: metav1.ConditionFalse,
			podsReady:      true,
			postCondition:  metav1.ConditionTrue,
		},
		{
			name:           "Pods Already Ready",
			readyCondition: metav1.ConditionTrue,
			podsReady:      true,
			postCondition:  metav1.ConditionTrue,
		},
		{
			name:           "Pods Not Ready",
			readyCondition: metav1.ConditionFalse,
			podsReady:      false,
			postCondition:  metav1.ConditionFalse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta.SetStatusCondition(&stateMachine.Cluster.Status.Conditions, metav1.Condition{
				Type:   condition.CondPodsReady,
				Status: tt.readyCondition,
			})

			if tt.readyCondition == metav1.ConditionFalse {
				wekaClusterService.EXPECT().IsContainersReady(gomock.Any(), gomock.Any()).Return(tt.podsReady, nil).Times(1)
			}
			os.Setenv("OPERATOR_DEV_MODE", "true")

			result, err := podsNotCreatedCondition.Handle(context.Background())
			if err != nil {
				t.Errorf("expected error to be nil, was %v", err)
			}
			if result.Requeue {
				t.Log("expected result to not requeue")
			}

			postCondition := meta.FindStatusCondition(stateMachine.Cluster.Status.Conditions, condition.CondPodsReady)
			if postCondition.Status != tt.postCondition {
				t.Errorf("expected condition status to be %v, was %v", tt.postCondition, postCondition.Status)
			}
		})
	}
}

func TestClusterNotCreatedCondition(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	wekaClusterService.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	containers := []*wekav1alpha1.WekaContainer{}
	wekaClusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(containers, nil).AnyTimes()

	stateMachine.WekaClusterService = wekaClusterService

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Status().Return(statusWriter).AnyTimes()
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	condition := &ClusterNotCreatedCondition{ReconcilerStateMachine: stateMachine}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	result, err := condition.Handle(context.Background())
	if err != nil {
		t.Errorf("expected error to be nil")
	}
	if result.Requeue {
		t.Log("expected result to not requeue")
	}
}

func TestContainersNotJoinedCondition(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Status().Return(statusWriter).AnyTimes()
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	containers := []*wekav1alpha1.WekaContainer{}
	wekaClusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(containers, nil).AnyTimes()
	wekaClusterService.EXPECT().EnsureClusterContainerIds(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	stateMachine.WekaClusterService = wekaClusterService
	condition := &ContainersNotJoinedCondition{ReconcilerStateMachine: stateMachine}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	result, err := condition.Handle(context.Background())
	if err != nil {
		t.Errorf("expected error to be nil")
	}
	if result.Requeue {
		t.Log("expected result to not requeue")
	}
}

func TestDrivesNotAddedCondition(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Status().Return(statusWriter).AnyTimes()
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	containers := []*wekav1alpha1.WekaContainer{}
	wekaClusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(containers, nil).AnyTimes()
	stateMachine.WekaClusterService = wekaClusterService

	condition := &DrivesNotAddedCondition{ReconcilerStateMachine: stateMachine}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	result, err := condition.Handle(context.Background())
	if err != nil {
		t.Errorf("expected error to be nil")
	}
	if result.Requeue {
		t.Errorf("expected result to not requeue")
	}
}

func TestIoNotStartedCondition(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	wekaClusterService := mocks.NewMockWekaClusterService(ctrl)
	containers := []*wekav1alpha1.WekaContainer{}
	wekaClusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(containers, nil).AnyTimes()
	wekaClusterService.EXPECT().StartIo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	stateMachine.WekaClusterService = wekaClusterService

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Status().Return(statusWriter).AnyTimes()
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	condition := &IoNotStartedCondition{ReconcilerStateMachine: stateMachine}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	result, err := condition.Handle(context.Background())
	if err != nil {
		t.Errorf("expected error to be nil")
	}
	if result.Requeue {
		t.Log("expected result to not requeue")
	}
}

func TestSecretsNotAppliedCondition(t *testing.T) {
	ctx, _, done := instrumentation.GetLogSpan(context.Background(), "TestSecretsNotAppliedCondition")
	defer done()

	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	subject := &SecretsNotAppliedCondition{ReconcilerStateMachine: stateMachine}

	os.Setenv("OPERATOR_DEV_MODE", "true")

	clusterService := mocks.NewMockWekaClusterService(ctrl)
	clusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	credentialsService := mocks.NewMockCredentialsService(ctrl)
	credentialsService.EXPECT().ApplyClusterCredentials(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	client := mocks.NewMockClient(ctrl)
	client.EXPECT().Status().Return(statusWriter).AnyTimes()

	stateMachine.WekaClusterService = clusterService
	stateMachine.CredentialsService = credentialsService
	stateMachine.Client = client

	tests := []struct {
		name           string
		prerequisities metav1.ConditionStatus
		status         metav1.ConditionStatus
		requeue        bool
		afterCondition metav1.ConditionStatus
	}{
		{
			name:           "Secrets Applied",
			prerequisities: metav1.ConditionTrue,
			status:         metav1.ConditionTrue,
			requeue:        false,
			afterCondition: metav1.ConditionTrue,
		},
		{
			name:           "Secrets Not Applied",
			prerequisities: metav1.ConditionTrue,
			status:         metav1.ConditionFalse,
			requeue:        false,
			afterCondition: metav1.ConditionTrue,
		},
		{
			name:           "Prerequisities Not Met",
			prerequisities: metav1.ConditionFalse,
			status:         metav1.ConditionFalse,
			requeue:        true,
			afterCondition: metav1.ConditionFalse,
		},
		{
			name:           "Invalid state",
			prerequisities: metav1.ConditionFalse,
			status:         metav1.ConditionTrue,
			requeue:        false,
			afterCondition: metav1.ConditionFalse,
		},
	}
	for _, tt := range tests {
		prerequisities := []string{
			condition.CondClusterSecretsCreated().String(),
			condition.CondPodsCreated,
			condition.CondPodsReady,
			condition.CondClusterCreated,
			condition.CondJoinedCluster,
			condition.CondDrivesAdded,
			condition.CondIoStarted,
		}
		for _, prerequisite := range prerequisities {
			meta.SetStatusCondition(&stateMachine.Cluster.Status.Conditions, metav1.Condition{
				Type:   prerequisite,
				Status: tt.prerequisities,
			})
		}
		t.Run(tt.name, func(t *testing.T) {
			ctx, _, done := instrumentation.GetLogSpan(ctx, tt.name)
			defer done()

			meta.SetStatusCondition(&stateMachine.Cluster.Status.Conditions, metav1.Condition{
				Type:   condition.CondClusterSecretsApplied().String(),
				Status: tt.status,
			})

			result, err := subject.Handle(ctx)
			if err != nil {
				t.Errorf("expected error to be nil")
			}
			if result.Requeue != tt.requeue {
				t.Errorf("expected result to requeue %v, was %v", tt.requeue, result.Requeue)
			}

			afterCondition := meta.FindStatusCondition(stateMachine.Cluster.Status.Conditions, condition.CondClusterSecretsCreated().String())
			if afterCondition.Status != tt.afterCondition {
				t.Errorf("expected condition status to be %v, was %v", tt.afterCondition, afterCondition.Status)
			}
		})
	}
}
