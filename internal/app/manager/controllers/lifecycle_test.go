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
		test.phase.EXPECT().String().Return(test.name)
		test.phase.EXPECT().IsCompleted().Return(test.completed)
		test.phase.EXPECT().IsReady().Return(test.ready).AnyTimes()
		test.phase.EXPECT().Handle(gomock.Any()).Return(test.requeue, test.expected).AnyTimes()

		phases := []ReconciliationPhase{test.phase}
		stateMachine := TestingReconcilerStateMachine(t)
		stateMachine.PhasesList = &phases
		result, err := stateMachine.RunLoop(context.Background())
		if err != test.expected {
			t.Errorf("expected error to be %v, was %v", test.expected, err)
		}
		if result.Requeue != test.requeue.Requeue {
			t.Errorf("%s: expected result to requeue %v, was %v", test.name, test.requeue, result.Requeue)
		}
	}
}

func TestPhases(t *testing.T) {
	stateMachine := TestingReconcilerStateMachine(t)
	phases := stateMachine.Phases()
	if len(phases) == 0 {
		t.Errorf("expected phases to be non-empty")
	}
}

func TestGetInitState(t *testing.T) {
	testEnv, err := setupTestEnv(context.Background())
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
	}
	ctx, _, done := instrumentation.GetLogSpan(testEnv.Ctx, "TestGetInitState")
	defer done()

	stateMachine := TestingReconcilerStateMachine(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testingClient := mocks.NewMockClient(ctrl)
	stateMachine.Client = testingClient

	statusWriter := mocks.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
	testingClient.EXPECT().Status().Return(statusWriter)
	testingClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
	testingClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	state, error := stateMachine.GetInitState()
	if error != nil {
		t.Errorf("expected error to be nil")
	}
	if state == nil {
		t.Errorf("expected state to be non-nil")
	}

	state.HandleInit(ctx)

	state, error = stateMachine.GetInitState()
	if error == nil {
		t.Errorf("expected error to be 'already initialized'")
	}
	if state != nil {
		t.Errorf("expected state to be nil")
	}

	err = teardownTestEnv(testEnv)
	if err != nil {
		t.Fatalf("failed to teardown test environment: %v", err)
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

	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	condition := &SecretsNotCreatedCondition{startingState}

	if err := condition.Handle(context.Background()); err != nil {
		t.Errorf("expected error to be nil")
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

	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	podsNotCreatedCondition := &PodsNotCreatedCondition{startingState}

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

			if err := podsNotCreatedCondition.Handle(context.Background()); err != nil {
				t.Errorf("expected error to be nil, was %v", err)
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

	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	condition := &ClusterNotCreatedCondition{startingState}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	if err := condition.Handle(context.Background()); err != nil {
		t.Errorf("expected error to be nil")
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
	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	condition := &ContainersNotJoinedCondition{startingState}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	if err := condition.Handle(context.Background()); err != nil {
		t.Errorf("expected error to be nil")
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

	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	condition := &DrivesNotAddedCondition{startingState}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	if err := condition.Handle(context.Background()); err != nil {
		t.Errorf("expected error to be nil")
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

	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	condition := &IoNotStartedCondition{startingState}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	if err := condition.Handle(context.Background()); err != nil {
		t.Errorf("expected error to be nil")
	}
}

func TestSecretsNotAppliedCondition(t *testing.T) {
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
	// wekaClusterService.EXPECT().GetUsernameAndPassword(gomock.Any(), gomock.Any(), gomock.Any()).Return("admin", "password", nil).AnyTimes()
	// wekaClusterService.EXPECT().ApplyClusterCredentials(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	containers := []*wekav1alpha1.WekaContainer{}
	wekaClusterService.EXPECT().EnsureWekaContainers(gomock.Any(), gomock.Any()).Return(containers, nil).AnyTimes()

	credentialsService := mocks.NewMockCredentialsService(ctrl)
	stateMachine.CredentialsService = credentialsService
	credentialsService.EXPECT().ApplyClusterCredentials(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	stateMachine.WekaClusterService = wekaClusterService

	startingState := &PhaseStartingState{ReconcilerStateMachine: stateMachine}
	condition := &SecretsNotAppliedCondition{startingState}

	os.Setenv("OPERATOR_DEV_MODE", "true")
	if err := condition.Handle(context.Background()); err != nil {
		t.Errorf("expected error to be nil")
	}
}
