package operations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/pkg/util"
)

// AdhocContainerParams contains parameters needed to create an ad-hoc container
// for fetching feature flags. The key identifier is the image.
// Note: The container is always created in the operator's namespace for owner reference.
type AdhocContainerParams struct {
	Image           string
	Labels          map[string]string
	NodeSelector    map[string]string
	NodeAffinity    weka.NodeName
	ImagePullSecret string
	Tolerations     []corev1.Toleration
}

// GetFeatureFlagsViaAdhocOperation fetches feature flags for an image
// using an ad-hoc container. This is the canonical way to get feature flags
// when no running container is available to exec into.
type GetFeatureFlagsViaAdhocOperation struct {
	client         client.Client
	scheme         *runtime.Scheme
	params         *AdhocContainerParams
	adhocContainer *weka.WekaContainer
	featureFlags   *domain.FeatureFlags
}

// NewGetFeatureFlagsFromImage creates an operation to get feature flags for an image.
// This is the main constructor - just needs image and scheduling params.
func NewGetFeatureFlagsFromImage(
	mgr ctrl.Manager,
	params *AdhocContainerParams,
) *GetFeatureFlagsViaAdhocOperation {
	return &GetFeatureFlagsViaAdhocOperation{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
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
		&lifecycle.SimpleStep{Name: "WaitForCachedFlags", Run: o.WaitForCachedFlags},
	}
}

func (o *GetFeatureFlagsViaAdhocOperation) GetJsonResult() string {
	result := map[string]any{
		"feature_flags": o.featureFlags,
	}
	jsonBytes, _ := json.Marshal(result) //nolint:errcheck // marshal of known-serializable struct; error not possible
	return string(jsonBytes)
}

func (o *GetFeatureFlagsViaAdhocOperation) AdhocExists() bool {
	return o.adhocContainer != nil
}

func (o *GetFeatureFlagsViaAdhocOperation) GetAdhocContainer(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetAdhocContainer")
	defer logger.End()

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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureAdhocContainer")
	defer logger.End()

	instructions := &weka.Instructions{
		Type: weka.InstructionTypeFeatureFlagsUpdate,
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
			ServiceAccountName: config.Config.MaintenanceSaName,
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

func (o *GetFeatureFlagsViaAdhocOperation) WaitForCachedFlags(ctx context.Context) error {
	flags, err := waitForCachedFeatureFlags(ctx, o.params.Image)
	o.featureFlags = flags
	return err
}

func (o *GetFeatureFlagsViaAdhocOperation) GetFeatureFlags() *domain.FeatureFlags {
	return o.featureFlags
}
