package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

// AdhocContainerParams contains parameters needed to create an ad-hoc container
// for fetching feature flags. The key identifier is the image.
// Note: The container is always created in the operator's namespace for owner reference.
type AdhocContainerParams struct {
	Image              string
	Labels             map[string]string
	NodeSelector       map[string]string
	NodeAffinity       weka.NodeName
	ImagePullSecret    string
	Tolerations        []corev1.Toleration
	ServiceAccountName string
}

// GetFeatureFlagsViaAdhocOperation fetches feature flags for an image
// using an ad-hoc container. This is the canonical way to get feature flags
// when no running container is available to exec into.
type GetFeatureFlagsViaAdhocOperation struct {
	client         client.Client
	scheme         *runtime.Scheme
	params         AdhocContainerParams
	adhocContainer *weka.WekaContainer
	featureFlags   *domain.FeatureFlags
}

// NewGetFeatureFlagsFromImage creates an operation to get feature flags for an image.
// This is the main constructor - just needs image and scheduling params.
func NewGetFeatureFlagsFromImage(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	params AdhocContainerParams,
) *GetFeatureFlagsViaAdhocOperation {
	return &GetFeatureFlagsViaAdhocOperation{
		client: k8sClient,
		scheme: scheme,
		params: params,
	}
}

func (o *GetFeatureFlagsViaAdhocOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "GetFeatureFlagsViaAdhoc",
		Run:  AsRunFunc(o),
	}
}

func (o *GetFeatureFlagsViaAdhocOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetAdhocContainer", Run: o.GetAdhocContainer},
		&lifecycle.SimpleStep{Name: "EnsureAdhocContainer", Run: o.EnsureAdhocContainer, Predicates: lifecycle.Predicates{lifecycle.IsNotFunc(o.AdhocExists)}},
		&lifecycle.SimpleStep{Name: "PollAdhocResult", Run: o.PollAdhocResult},
		&lifecycle.SimpleStep{Name: "ProcessAdhocResult", Run: o.ProcessAdhocResult},
		&lifecycle.SimpleStep{Name: "CacheFeatureFlags", Run: o.CacheFeatureFlags},
		&lifecycle.SimpleStep{Name: "DeleteAdhocContainer", Run: o.DeleteAdhocContainer, Predicates: lifecycle.Predicates{o.AdhocExists}},
	}
}

func (o *GetFeatureFlagsViaAdhocOperation) GetJsonResult() string {
	result := map[string]any{
		"feature_flags": o.featureFlags,
	}
	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes)
}

func (o *GetFeatureFlagsViaAdhocOperation) AdhocExists() bool {
	return o.adhocContainer != nil
}

func (o *GetFeatureFlagsViaAdhocOperation) GetAdhocContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetAdhocContainer")
	defer end()

	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return fmt.Errorf("error getting operator namespace: %w", err)
	}

	var existing weka.WekaContainer
	containerName := GetFeatureFlagsAdhocContainerName(o.params.Image)

	err = o.client.Get(ctx, client.ObjectKey{
		Namespace: operatorNamespace,
		Name:      containerName,
	}, &existing)

	if apierrors.IsNotFound(err) {
		logger.Info("No existing ad-hoc container found", "container", containerName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting ad-hoc container: %w", err)
	}

	o.adhocContainer = &existing
	return nil
}

// GetFeatureFlagsAdhocContainerName returns the canonical name for a feature flags
// ad-hoc container based on image hash. Exported so both operations can share.
func GetFeatureFlagsAdhocContainerName(image string) string {
	imageHash := util.GetHash(image, 12)
	return fmt.Sprintf("weka-feature-flags-%s", imageHash)
}

func (o *GetFeatureFlagsViaAdhocOperation) EnsureAdhocContainer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureAdhocContainer")
	defer end()

	instructions := &weka.Instructions{
		Type: "feature-flags-update",
	}

	labels := map[string]string{
		"weka.io/mode": weka.WekaContainerModeAdhocOpWC,
	}
	labels = util.MergeMaps(o.params.Labels, labels)

	containerName := GetFeatureFlagsAdhocContainerName(o.params.Image)

	operatorDeployment, err := util.GetOperatorDeployment(ctx, o.client)
	if err != nil {
		return fmt.Errorf("error getting operator deployment: %w", err)
	}

	// Create ad-hoc container in operator's namespace for owner reference to work
	newContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: operatorDeployment.Namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Mode:               weka.WekaContainerModeAdhocOpWC,
			Port:               weka.StaticPortAdhocyWCOperations,
			AgentPort:          weka.StaticPortAdhocyWCOperationsAgent,
			NodeSelector:       o.params.NodeSelector,
			NodeAffinity:       o.params.NodeAffinity,
			Image:              o.params.Image,
			ImagePullSecret:    o.params.ImagePullSecret,
			Instructions:       instructions,
			Tolerations:        o.params.Tolerations,
			ServiceAccountName: o.params.ServiceAccountName,
		},
	}

	err = controllerutil.SetControllerReference(operatorDeployment, newContainer, o.scheme)
	if err != nil {
		return err
	}

	err = o.client.Create(ctx, newContainer)
	if err != nil {
		return err
	}

	o.adhocContainer = newContainer
	logger.Info("Created ad-hoc container for feature flags", "container", containerName, "image", o.params.Image)
	return nil
}

func (o *GetFeatureFlagsViaAdhocOperation) PollAdhocResult(ctx context.Context) error {
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

func (o *GetFeatureFlagsViaAdhocOperation) ProcessAdhocResult(ctx context.Context) error {
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
	logger.Info("Processed feature flags from ad-hoc container", "image", o.params.Image)
	return nil
}

func (o *GetFeatureFlagsViaAdhocOperation) CacheFeatureFlags(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CacheFeatureFlags")
	defer end()

	if o.featureFlags == nil {
		return errors.New("no feature flags to cache")
	}

	err := services.FeatureFlagsCache.SetFeatureFlags(ctx, o.params.Image, o.featureFlags)
	if err != nil {
		return err
	}

	logger.Info("Cached feature flags", "image", o.params.Image)
	return nil
}

func (o *GetFeatureFlagsViaAdhocOperation) DeleteAdhocContainer(ctx context.Context) error {
	if o.adhocContainer == nil {
		return nil
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "DeleteAdhocContainer")
	defer end()

	err := o.client.Delete(ctx, o.adhocContainer)
	if err != nil {
		logger.Info("Failed to delete ad-hoc container", "error", err)
		return nil
	}

	logger.Info("Deleted ad-hoc container")
	return nil
}

func (o *GetFeatureFlagsViaAdhocOperation) GetFeatureFlags() *domain.FeatureFlags {
	return o.featureFlags
}
