package umount

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type UmountOperation struct {
	client     client.Client
	scheme     *runtime.Scheme
	image      string
	pullSecret string
	container  *weka.WekaContainer
	ownerRef   client.Object
	mgr        ctrl.Manager
}

func NewUmountOperation(mgr ctrl.Manager, targetContainer *weka.WekaContainer) *UmountOperation {
	kclient := mgr.GetClient()
	return &UmountOperation{
		mgr:        mgr,
		client:     kclient,
		scheme:     mgr.GetScheme(),
		image:      config.Config.SignDrivesImage, // reuse sign drives image
		pullSecret: targetContainer.Spec.ImagePullSecret,
		container:  targetContainer,
		ownerRef:   targetContainer,
	}
}

func (o *UmountOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "Umount",
		Run:  operations.AsRunFunc(o),
	}
}

func (o *UmountOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "GetContainer", Run: o.GetContainer},
		{
			Name: "EnsureContainer",
			Run:  o.EnsureContainer,
		},
		{
			Name: "WaitResults",
			Run:  o.WaitResults,
		},
	}
}

func (o *UmountOperation) GetContainer(ctx context.Context) error {
	existing, err := discovery.GetOwnedContainers(ctx, o.client, o.ownerRef.GetUID(), o.ownerRef.GetNamespace(), weka.WekaContainerModeAdhocOp)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		o.container = existing[0]
	}
	return nil
}

func (o *UmountOperation) Cleanup(ctx context.Context) error {
	if o.container == nil {
		return nil
	}
	err := o.client.Delete(ctx, o.container)
	if err != nil {
		return errors.Wrap(err, "failed to delete container")
	}
	return nil
}

func (o *UmountOperation) EnsureContainer(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	instructions := &weka.Instructions{
		Type:    "umount",
		Payload: "{}",
	}

	labels := util.MergeMaps(o.ownerRef.GetLabels(), factory.RequiredAnyWekaContainerLabels(weka.WekaContainerModeAdhocOp))

	containerName := fmt.Sprintf("weka-umount-%s", o.container.GetNodeAffinity())
	newContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: o.ownerRef.GetNamespace(),
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Mode:            weka.WekaContainerModeAdhocOp,
			NodeAffinity:    o.container.GetNodeAffinity(),
			Image:           o.image,
			ImagePullSecret: o.pullSecret,
			Instructions:    instructions,
			Tolerations: []v1.Toleration{
				{
					Operator: v1.TolerationOpExists,
				},
			},
			HostPID: true,
		},
	}

	err := controllerutil.SetControllerReference(o.ownerRef, newContainer, o.scheme)
	if err != nil {
		return errors.Wrap(err, "failed to set controller reference")
	}

	err = o.client.Create(ctx, newContainer)
	if err != nil {
		return errors.Wrap(err, "failed to create container")
	}

	o.container = newContainer
	return nil
}

func (o UmountOperation) GetJsonResult() string {
	if o.container.Status.ExecutionResult == nil {
		return ""
	}
	return *o.container.Status.ExecutionResult
}

func (o *UmountOperation) WaitResults(ctx context.Context) error {
	if o.container.Status.ExecutionResult == nil {
		return errors.New("results are not ready")
	}
	return nil
}
