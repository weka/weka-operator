package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
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

// ReadFeatureFlagsViaExec reads feature flags off a container's image by exec-ing into it. Every
// path uses it, ad-hoc included: the flags come from the image, so nothing here depends on which
// weka_runtime.py the pod started with.
func ReadFeatureFlagsViaExec(ctx context.Context, execService exec.ExecService, container *weka.WekaContainer) (*domain.FeatureFlags, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ReadFeatureFlagsViaExec", "container", container.Name)
	defer logger.End()

	timeout := time.Second * 10

	executor, err := execService.GetExecutorWithTimeout(ctx, container, &timeout)
	if err != nil {
		return nil, fmt.Errorf("error getting executor: %w", err)
	}

	// Anything but exactly one spec fails rather than guessing, since the result is cached for the
	// image operator-wide. The second directory is the shared-weka-version bind mount over /opt/weka.
	cmd := []string{"sh", "-c",
		`set -- /opt/weka/dist/release/*.spec; ` +
			`[ -e "$1" ] || set -- /shared-weka-version/opt-weka/dist/release/*.spec; ` +
			`[ "$#" -eq 1 ] && [ -e "$1" ] && cat "$1"`}
	stdout, _, err := executor.ExecNamed(ctx, "ReadReleaseSpec", cmd)
	if err != nil {
		return nil, fmt.Errorf("error reading release spec: %w", err)
	}
	// Catches a spec file that exists but is empty, which would otherwise decode to an empty
	// bitmap and read every flag as false.
	if stdout.Len() == 0 {
		return nil, errors.New("release spec is empty")
	}

	flags, err := domain.ParseReleaseSpec(stdout.Bytes())
	if err != nil {
		return nil, err
	}

	logger.Info("Successfully read feature flags from container", "flags", flags)
	return flags, nil
}

// waitForCachedFeatureFlags waits for the ad-hoc container's own reconcile to cache the flags
// (processResults). Requesters never exec into that pod: it deletes itself shortly after.
func waitForCachedFeatureFlags(ctx context.Context, image string) (*domain.FeatureFlags, error) {
	flags, err := services.GetFeatureFlags(ctx, image)
	if errors.Is(err, services.ErrFeatureFlagsNotCached) {
		// A stuck wait means that container's result step keeps failing; its status has the cause.
		err = fmt.Errorf("waiting for ad-hoc container %s to cache feature flags of %s: %w", GetFeatureFlagsAdhocContainerName(image), image, err)
		return nil, lifecycle.NewWaitErrorWithDuration(err, 15*time.Second)
	}
	return flags, err
}

func NewGetFeatureFlagsOperation(
	mgr ctrl.Manager,
	restClient rest.Interface,
	currentContainer *weka.WekaContainer,
) *GetFeatureFlagsOperation {
	kclient := mgr.GetClient()
	restConfig := mgr.GetConfig()

	image := currentContainer.Status.LastAppliedImage
	if image == "" {
		image = currentContainer.Spec.Image
	}

	return &GetFeatureFlagsOperation{
		client:           kclient,
		execService:      exec.NewExecService(restClient, restConfig),
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
		&lifecycle.SimpleStep{Name: "CacheFeatureFlags", Run: o.CacheFeatureFlags, Predicates: lifecycle.Predicates{lifecycle.IsNotFunc(o.NeedsFallback)}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{Name: "EnsureAdhocContainer", Run: o.EnsureAdhocContainer, Predicates: lifecycle.Predicates{lifecycle.IsNotFunc(o.AdhocExists)}},
		&lifecycle.SimpleStep{Name: "WaitForCachedFlags", Run: o.WaitForCachedFlags},
	}
}

func (o *GetFeatureFlagsOperation) GetJsonResult() string {
	result := map[string]any{
		"feature_flags": o.featureFlags,
	}
	jsonBytes, _ := json.Marshal(result) //nolint:errcheck // marshal of known-serializable struct; error not possible
	return string(jsonBytes)
}

func (o *GetFeatureFlagsOperation) NeedsFallback() bool {
	return o.featureFlags == nil
}

func (o *GetFeatureFlagsOperation) AdhocExists() bool {
	return o.adhocContainer != nil
}

func (o *GetFeatureFlagsOperation) ExecCurrentContainer(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	flags, err := ReadFeatureFlagsViaExec(ctx, o.execService, o.currentContainer)
	if err != nil {
		logger.Info("Failed to read feature flags from current container, will try fallback", "error", err)
		return nil // Don't fail, just try fallback
	}

	o.featureFlags = flags
	logger.Info("Successfully read feature flags from current container")
	return nil
}

func (o *GetFeatureFlagsOperation) ExecActiveContainer(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

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

	flags, err := ReadFeatureFlagsViaExec(ctx, o.execService, activeContainer)
	if err != nil {
		logger.Info("Failed to read feature flags from active container, will try ad-hoc container", "error", err)
		return nil // Don't fail, just try fallback
	}

	o.featureFlags = flags
	logger.Info("Successfully read feature flags from active container")
	return nil
}

// GetAdhocContainer gets existing ad-hoc container if any
func (o *GetFeatureFlagsOperation) GetAdhocContainer(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetAdhocContainer")
	defer logger.End()

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
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureAdhocContainer")
	defer logger.End()

	instructions := &weka.Instructions{
		Type: weka.InstructionTypeFeatureFlagsUpdate,
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
			Mode:               weka.WekaContainerModeAdhocOpWC,
			Port:               weka.StaticPortAdhocyWCOperations,
			AgentPort:          weka.StaticPortAdhocyWCOperationsAgent,
			NodeAffinity:       o.currentContainer.GetNodeAffinity(),
			Image:              o.image,
			ImagePullSecret:    o.currentContainer.Spec.ImagePullSecret,
			Instructions:       instructions,
			Tolerations:        o.currentContainer.Spec.Tolerations,
			ServiceAccountName: config.Config.MaintenanceSaName,
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

func (o *GetFeatureFlagsOperation) WaitForCachedFlags(ctx context.Context) error {
	flags, err := waitForCachedFeatureFlags(ctx, o.image)
	o.featureFlags = flags
	return err
}

// CacheFeatureFlags caches the feature flags
func (o *GetFeatureFlagsOperation) CacheFeatureFlags(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "CacheFeatureFlags")
	defer logger.End()

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
