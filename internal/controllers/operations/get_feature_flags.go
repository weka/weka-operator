package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
)

type GetFeatureFlagsOperation struct {
	client           client.Client
	execService      exec.ExecService
	scheme           *runtime.Scheme
	currentContainer *weka.WekaContainer
	image            string
	namespace        string
	adhocContainer   *weka.WekaContainer
	featureFlags     *domain.FeatureFlags
}

type FeatureFlagsResult struct {
	FeatureFlags *domain.FeatureFlags `json:"feature_flags"`
}

func NewGetFeatureFlagsOperation(
	mgr ctrl.Manager,
	restClient rest.Interface,
	currentContainer *weka.WekaContainer,
) *GetFeatureFlagsOperation {
	kclient := mgr.GetClient()
	config := mgr.GetConfig()

	image := currentContainer.Status.LastAppliedImage
	if image == "" {
		image = currentContainer.Spec.Image
	}

	return &GetFeatureFlagsOperation{
		client:           kclient,
		execService:      exec.NewExecService(restClient, config),
		scheme:           mgr.GetScheme(),
		currentContainer: currentContainer,
		image:            image,
		namespace:        currentContainer.Namespace,
	}
}

func (o *GetFeatureFlagsOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "GetFeatureFlags",
		Run:  AsRunFunc(o),
	}
}

func (o *GetFeatureFlagsOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetAdhocContainer", Run: o.GetAdhocContainer},
		&lifecycle.SimpleStep{Name: "ExecCurrentContainer", Run: o.ExecCurrentContainer, Predicates: lifecycle.Predicates{lifecycle.IsNotFunc(o.AdhocExists)}},
		&lifecycle.SimpleStep{Name: "ExecActiveContainer", Run: o.ExecActiveContainer, Predicates: lifecycle.Predicates{o.NeedsFallback, lifecycle.IsNotFunc(o.AdhocExists)}},
		&lifecycle.SimpleStep{Name: "DeleteOnDone", Run: o.DeleteAdhocContainer, Predicates: lifecycle.Predicates{o.AdhocIsDone}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{Name: "EnsureAdhocContainer", Run: o.EnsureAdhocContainer, Predicates: lifecycle.Predicates{o.NeedsFallback, lifecycle.IsNotFunc(o.AdhocExists)}},
		&lifecycle.SimpleStep{Name: "PollAdhocResult", Run: o.PollAdhocResult, Predicates: lifecycle.Predicates{o.NeedsFallback}},
		&lifecycle.SimpleStep{Name: "ProcessAdhocResult", Run: o.ProcessAdhocResult, Predicates: lifecycle.Predicates{o.NeedsFallback}},
		&lifecycle.SimpleStep{Name: "CacheFeatureFlags", Run: o.CacheFeatureFlags},
		&lifecycle.SimpleStep{Name: "DeleteAdhocContainer", Run: o.DeleteAdhocContainer, Predicates: lifecycle.Predicates{o.AdhocExists}},
	}
}

func (o *GetFeatureFlagsOperation) GetJsonResult() string {
	result := map[string]any{
		"feature_flags": o.featureFlags,
	}
	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes)
}

func (o *GetFeatureFlagsOperation) NeedsFallback() bool {
	return o.featureFlags == nil
}

func (o *GetFeatureFlagsOperation) AdhocExists() bool {
	return o.adhocContainer != nil
}

func (o *GetFeatureFlagsOperation) AdhocIsDone() bool {
	return o.adhocContainer != nil && o.adhocContainer.Status.ExecutionResult != nil && o.featureFlags != nil
}

func (o *GetFeatureFlagsOperation) ExecCurrentContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	flags, err := o.readFeatureFlagsFromContainer(ctx, o.currentContainer)
	if err != nil {
		logger.Info("Failed to read feature flags from current container, will try fallback", "error", err)
		return nil // Don't fail, just try fallback
	}

	o.featureFlags = flags
	logger.Info("Successfully read feature flags from current container")
	return nil
}

func (o *GetFeatureFlagsOperation) ExecActiveContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// Get all containers with the same lastAppliedImage
	containerList := &weka.WekaContainerList{}
	err := o.client.List(ctx, containerList, client.InNamespace(o.namespace))
	if err != nil {
		logger.Info("Failed to list containers", "error", err)
		return nil // Don't fail, just try fallback
	}

	// Filter containers with the same lastAppliedImage
	var matchingContainers []*weka.WekaContainer
	for i := range containerList.Items {
		container := &containerList.Items[i]
		if !container.IsWekaContainer() {
			continue
		}
		if container.Status.LastAppliedImage == o.image {
			matchingContainers = append(matchingContainers, container)
		}
	}

	if len(matchingContainers) == 0 {
		logger.Info("No active containers with matching image found")
		return nil // Don't fail, just try fallback
	}

	// Select an active container
	activeContainer := discovery.SelectActiveContainer(matchingContainers)
	if activeContainer == nil {
		logger.Info("No active container found")
		return nil // Don't fail, just try fallback
	}

	flags, err := o.readFeatureFlagsFromContainer(ctx, activeContainer)
	if err != nil {
		logger.Info("Failed to read feature flags from active container, will try ad-hoc container", "error", err)
		return nil // Don't fail, just try fallback
	}

	o.featureFlags = flags
	logger.Info("Successfully read feature flags from active container")
	return nil
}

// readFeatureFlagsFromContainer reads feature flags from a container by exec-ing into it
func (o *GetFeatureFlagsOperation) readFeatureFlagsFromContainer(ctx context.Context, container *weka.WekaContainer) (*domain.FeatureFlags, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "readFeatureFlagsFromContainer", "container", container.Name)
	defer end()

	timeout := time.Second * 10

	executor, err := o.execService.GetExecutorWithTimeout(ctx, container, &timeout)
	if err != nil {
		return nil, fmt.Errorf("error getting executor: %w", err)
	}

	// Read the feature flags JSON file
	featureFlagsPath := "/opt/weka/k8s-runtime/feature_flags.json"
	cmd := []string{"cat", featureFlagsPath}
	stdout, _, err := executor.ExecNamed(ctx, "ReadFeatureFlagsFile", cmd)
	if err != nil {
		return nil, fmt.Errorf("error reading feature flags file: %w", err)
	}

	// Parse JSON to FeatureFlags struct
	var flags domain.FeatureFlags
	if err := json.Unmarshal(stdout.Bytes(), &flags); err != nil {
		return nil, fmt.Errorf("error parsing feature_flags.json: %w", err)
	}

	logger.Info("Successfully read feature flags from container", "flags", flags)
	return &flags, nil
}

// GetAdhocContainer gets existing ad-hoc container if any
func (o *GetFeatureFlagsOperation) GetAdhocContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetAdhocContainer")
	defer end()

	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return fmt.Errorf("error getting operator namespace: %w", err)
	}

	var existing weka.WekaContainer

	containerName := o.getAdhocContainerName()

	err = o.client.Get(ctx, client.ObjectKey{
		Namespace: operatorNamespace,
		Name:      containerName,
	}, &existing)

	if apierrors.IsNotFound(err) {
		logger.Info("No existing ad-hoc container found", "container", containerName)
		return nil // No existing container
	}
	if err != nil {
		return fmt.Errorf("error getting ad-hoc container: %w", err)
	}

	o.adhocContainer = &existing

	return nil
}

func (o *GetFeatureFlagsOperation) getAdhocContainerName() string {
	return GetFeatureFlagsAdhocContainerName(o.image)
}

// EnsureAdhocContainer creates an ad-hoc container to read feature flags
func (o *GetFeatureFlagsOperation) EnsureAdhocContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureAdhocContainer")
	defer end()

	instructions := &weka.Instructions{
		Type: "feature-flags-update",
	}

	labels := map[string]string{
		"weka.io/mode": weka.WekaContainerModeAdhocOpWC,
	}
	labels = util.MergeMaps(o.currentContainer.GetLabels(), labels)

	containerName := o.getAdhocContainerName()

	newContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: o.namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Mode:            weka.WekaContainerModeAdhocOpWC,
			Port:            weka.StaticPortAdhocyWCOperations,
			AgentPort:       weka.StaticPortAdhocyWCOperationsAgent,
			NodeAffinity:    o.currentContainer.GetNodeAffinity(),
			Image:           o.image,
			ImagePullSecret: o.currentContainer.Spec.ImagePullSecret,
			Instructions:    instructions,
			Tolerations:     o.currentContainer.Spec.Tolerations,
			// ServiceAccountName intentionally not set - ad-hoc container runs in operator namespace
			// where the original container's SA doesn't exist
		},
	}

	operatorDeployment, err := util.GetOperatorDeployment(ctx, o.client)
	if err != nil {
		return fmt.Errorf("error getting operator deployment: %w", err)
	}

	// Create ad-hoc container in operator's namespace for owner reference to work
	newContainer.Namespace = operatorDeployment.Namespace

	// Set owner reference to operator deployment for cleanup
	err = controllerutil.SetControllerReference(operatorDeployment, newContainer, o.scheme)
	if err != nil {
		return err
	}

	err = o.client.Create(ctx, newContainer)
	if err != nil {
		return err
	}

	o.adhocContainer = newContainer
	logger.Info("Created ad-hoc container for feature flags", "container", containerName)
	return nil
}

// PollAdhocResult waits for the ad-hoc container to complete
func (o *GetFeatureFlagsOperation) PollAdhocResult(ctx context.Context) error {
	// Refresh container status
	err := o.client.Get(ctx, client.ObjectKey{
		Namespace: o.adhocContainer.Namespace,
		Name:      o.adhocContainer.Name,
	}, o.adhocContainer)
	if err != nil {
		return err
	}

	if o.adhocContainer.Status.ExecutionResult == nil {
		return lifecycle.NewWaitError(fmt.Errorf("ad-hoc container execution result not ready"))
	}

	return nil
}

// ProcessAdhocResult processes the result from the ad-hoc container
func (o *GetFeatureFlagsOperation) ProcessAdhocResult(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ProcessAdhocResult")
	defer end()

	if o.adhocContainer == nil || o.adhocContainer.Status.ExecutionResult == nil {
		return errors.New("no ad-hoc container result to process")
	}

	var result FeatureFlagsResult
	err := json.Unmarshal([]byte(*o.adhocContainer.Status.ExecutionResult), &result)
	if err != nil {
		return fmt.Errorf("error parsing ad-hoc container result: %w", err)
	}

	o.featureFlags = result.FeatureFlags
	logger.Info("Successfully processed feature flags from ad-hoc container", "flags", o.featureFlags)
	return nil
}

// CacheFeatureFlags caches the feature flags
func (o *GetFeatureFlagsOperation) CacheFeatureFlags(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CacheFeatureFlags")
	defer end()

	if o.featureFlags == nil {
		return errors.New("no feature flags to cache")
	}

	err := services.SetFeatureFlags(ctx, o.image, o.featureFlags)
	if err != nil {
		return err
	}

	logger.Info("Successfully cached feature flags", "image", o.image)
	return nil
}

// DeleteAdhocContainer deletes the ad-hoc container
func (o *GetFeatureFlagsOperation) DeleteAdhocContainer(ctx context.Context) error {
	if o.adhocContainer == nil {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DeleteAdhocContainer")
	defer end()

	err := o.client.Delete(ctx, o.adhocContainer)
	if err != nil {
		logger.Info("Failed to delete ad-hoc container", "error", err)
		// Don't fail the operation if cleanup fails
		return nil
	}

	logger.Info("Deleted ad-hoc container")
	return nil
}
