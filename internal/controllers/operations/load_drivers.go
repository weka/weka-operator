package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pkg/errors"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LoadDrivers struct {
	mgr                 ctrl.Manager
	client              client.Client
	kubeService         kubernetes.KubeService
	scheme              *runtime.Scheme
	containerDetails    weka.WekaContainerDetails
	node                *v1.Node
	distServiceEndpoint string
	container           *weka.WekaContainer
	namespace           string
	results             *string
	isFrontend          bool // defines whether we should enforce latest version, or suffice with any version
}

func NewLoadDrivers(mgr ctrl.Manager, node *v1.Node, wekaContainerDetails weka.WekaContainerDetails, distServiceEndpoint string, isFrontend bool) *LoadDrivers {
	kclient := mgr.GetClient()
	ns, _ := util.GetPodNamespace()
	return &LoadDrivers{
		mgr:                 mgr,
		client:              kclient,
		kubeService:         kubernetes.NewKubeService(kclient),
		scheme:              mgr.GetScheme(),
		containerDetails:    wekaContainerDetails,
		node:                node,
		distServiceEndpoint: distServiceEndpoint,
		namespace:           ns,
		isFrontend:          isFrontend,
	}
}

func (o *LoadDrivers) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "LoadDrivers",
		Run:  AsRunFunc(o),
	}
}

func (o *LoadDrivers) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "GetCurrentContainer", Run: o.GetCurrentContainers},
		{Name: "CleanupIfLoaded", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsLoaded}, ContinueOnPredicatesFalse: true, FinishOnSuccess: true},
		//TODO: We might be deleting container created by client here, IsLoaded would be true on mismatch. Just timing wise, this is unlikely to happen, as backends supposed to be upgraded
		{Name: "CreateContainer", Run: o.CreateContainer, Predicates: lifecycle.Predicates{o.HasNotContainer}, ContinueOnPredicatesFalse: true},
		{Name: "PollResults", Run: o.PollResults},
		{Name: "ProcessResult", Run: o.ProcessResult},
		{Name: "DeleteContainers", Run: o.DeleteContainers},
	}
}

func (o *LoadDrivers) GetJsonResult() string {
	panic("not implemented due to no interfaced use")
}

func (o *LoadDrivers) IsLoaded() bool {
	annotations := o.node.Annotations
	current, ok := annotations["weka.io/drivers-loaded"]
	if ok && !o.isFrontend {
		return true
	}
	if current == o.GetExpectedDriversVersion() {
		return true
	}
	return false
}

func (o *LoadDrivers) GetExpectedDriversVersion() string {
	return fmt.Sprintf("%s:%s", o.containerDetails.Image, o.node.Status.NodeInfo.BootID)
}

func (o *LoadDrivers) ExitIfLoaded(ctx context.Context) error {
	return nil
}

func (o *LoadDrivers) GetCurrentContainers(ctx context.Context) error {
	primaryNamespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}
	containers, err := discovery.GetOwnedContainers(ctx, o.client, o.node.GetUID(), primaryNamespace, weka.WekaContainerModeDriversLoader)
	if err != nil {
		return err
	}

	if len(containers) == 1 {
		o.container = containers[0]
	} else {
		if len(containers) > 1 {
			return fmt.Errorf("more than one loader container found")
		}
	}

	return nil
}

func (o *LoadDrivers) HasNotContainer() bool {
	return o.container == nil
}

func (o *LoadDrivers) CreateContainer(ctx context.Context) error {
	serviceAccountName := os.Getenv("WEKA_OPERATOR_MAINTENANCE_SA_NAME")
	if serviceAccountName == "" {
		return fmt.Errorf("cannot create driver loader container, WEKA_OPERATOR_MAINTENANCE_SA_NAME is not defined")
	}

	loaderContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("weka-drivers-loader-%s", o.node.UID),
			Namespace: o.namespace,
			Labels: map[string]string{
				"weka.io/mode": weka.WekaContainerModeDriversLoader, // need to make this somehow more generic and not per place
			},
		},
		Spec: weka.WekaContainerSpec{
			Image:               o.containerDetails.Image,
			Mode:                weka.WekaContainerModeDriversLoader,
			ImagePullSecret:     o.containerDetails.ImagePullSecret,
			Hugepages:           0,
			NodeAffinity:        weka.NodeName(o.node.Name),
			DriversDistService:  o.distServiceEndpoint,
			TracesConfiguration: weka.GetDefaultTracesConfiguration(),
			Tolerations:         o.containerDetails.Tolerations,
			ServiceAccountName:  serviceAccountName,
		},
	}
	err := ctrl.SetControllerReference(o.node, loaderContainer, o.scheme)
	if err != nil {
		return err
	}

	err = o.client.Create(ctx, loaderContainer)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return lifecycle.NewWaitError(fmt.Errorf("container already exists"))
		}
		return err
	}
	o.container = loaderContainer
	return nil
}

func (o *LoadDrivers) PollResults(ctx context.Context) error {
	if o.container.Status.ExecutionResult == nil {
		return lifecycle.NewWaitError(fmt.Errorf("container execution result is not ready"))
	}
	return nil
}

type DriveLoadResults struct {
	Err    string `json:"err"`
	Loaded bool   `json:"drivers_loaded"`
}

func (o *LoadDrivers) ProcessResult(ctx context.Context) error {
	loadResults := &DriveLoadResults{}
	err := json.Unmarshal([]byte(*o.container.Status.ExecutionResult), loadResults)
	if err != nil {
		return errors.Wrap(err, "Failed to unmarshal results")
	}

	if loadResults.Err != "" {
		ret := fmt.Errorf("error loading drivers: %s, re-creating container", loadResults.Err)
		_ = o.DeleteContainers(ctx)
		return ret
	}

	if !loadResults.Loaded {
		_ = o.DeleteContainers(ctx)
		return fmt.Errorf("drivers not loaded, with no err set")
	}

	annotations := o.node.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["weka.io/drivers-loaded"] = o.GetExpectedDriversVersion()
	o.node.Annotations = annotations
	err = o.client.Update(ctx, o.node)
	if err != nil {
		return lifecycle.NewWaitError(err)
	}

	return nil
}

func (o *LoadDrivers) DeleteContainers(ctx context.Context) error {
	if o.container != nil {
		err := o.client.Delete(ctx, o.container)
		if err != nil {
			return err
		}
	}
	return nil
}
