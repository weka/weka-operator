package wekacontainer

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/csi"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/pkg/util"
)

func GetCsiNodeServerPodName(containerName string) string {
	return fmt.Sprintf("%s-csi-node", containerName)
}

func (r *containerReconcilerLoop) WekaContainerManagesCsi() bool {
	return r.container.IsClientContainer() && config.Config.Csi.Enabled
}

func CsiSteps(r *containerReconcilerLoop) []lifecycle.Step {
	container := r.container

	return []lifecycle.Step{
		&lifecycle.GroupedSteps{
			Name: "CsiInstallation",
			Predicates: lifecycle.Predicates{
				r.WekaContainerManagesCsi,
			},
			Steps: []lifecycle.Step{
				&lifecycle.SimpleStep{
					State: &lifecycle.State{
						Name: condition.CondCsiDeployed,
					},
					SkipStepStateCheck: true,
					Run:                r.DeployCsiNodeServerPod,
					Predicates: lifecycle.Predicates{
						r.shouldDeployCsiNodeServerPod,
						lifecycle.IsNotFunc(r.PodNotSet),
					},
				},
				&lifecycle.SimpleStep{
					Run: r.ManageCsiTopologyLabels,
					Predicates: lifecycle.Predicates{
						lifecycle.IsTrueCondition(condition.CondCsiDeployed, &container.Status.Conditions),
					},
				},
			},
		},
		&lifecycle.SimpleStep{
			Run: r.CleanupCsiNodeServerPod,
			Predicates: lifecycle.Predicates{
				container.IsClientContainer,
				lifecycle.BoolValue(!config.Config.Csi.Enabled),
				lifecycle.IsTrueCondition(condition.CondCsiDeployed, &r.container.Status.Conditions),
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval:                    10 * time.Minute,
				DisableRandomPreSetInterval: true,
			},
			ContinueOnError: true,
		},
	}
}

func (r *containerReconcilerLoop) GetCSIGroup() string {
	if r.targetCluster != nil {
		return csi.GetGroupFromTargetCluster(r.targetCluster)
	}
	return csi.GetGroupFromClient(r.wekaClient)
}

func (r *containerReconcilerLoop) getCsiDriverName() string {
	return fmt.Sprintf("%s.weka.io", r.GetCSIGroup())
}

func (r *containerReconcilerLoop) DeployCsiNodeServerPod(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("node affinity is not set")
	}

	namespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}

	csiNodeName := GetCsiNodeServerPodName(r.container.Name)

	var csiNodeLabels map[string]string
	var csiNodeTolerations []corev1.Toleration
	var enforceTrustedHttps bool
	if r.wekaClient.Spec.CsiConfig != nil && r.wekaClient.Spec.CsiConfig.Advanced != nil {
		csiNodeLabels = r.wekaClient.Spec.CsiConfig.Advanced.NodeLabels
		csiNodeTolerations = r.wekaClient.Spec.CsiConfig.Advanced.NodeTolerations
		enforceTrustedHttps = r.wekaClient.Spec.CsiConfig.Advanced.EnforceTrustedHttps
	}
	labels := csi.GetCsiLabels(r.getCsiDriverName(), csi.CSINode, r.container.Labels, csiNodeLabels)
	tolerations := append(r.container.Spec.Tolerations, csiNodeTolerations...)
	// tolerate all NoSchedule taints for the CSI node pod
	tolerations = resources.ExpandNoScheduleTolerations(tolerations)
	// add NoExecute tolerations for common node taints
	tolerations = expandCsiNoExecuteTolerations(tolerations)

	targetHash, err := r.calculateCSINodeHash(enforceTrustedHttps, labels, tolerations)
	if err != nil {
		return errors.Wrap(err, "failed to calculate CSI node hash")
	}

	pod := &corev1.Pod{}
	err = r.Get(ctx, client.ObjectKey{Name: csiNodeName, Namespace: namespace}, pod)

	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating CSI node pod")
			podSpec := csi.NewCsiNodePod(
				csiNodeName,
				namespace,
				r.getCsiDriverName(),
				string(nodeName),
				labels,
				tolerations,
				enforceTrustedHttps,
				targetHash,
			)

			operatorDeployment, err := util.GetOperatorDeployment(ctx, r.Client)
			if err != nil {
				return errors.Wrap(err, "failed to get operator deployment")
			}

			// set owner reference to the operator deployment
			err = controllerutil.SetControllerReference(operatorDeployment, podSpec, r.Scheme)
			if err != nil {
				return err
			}

			if err = r.Create(ctx, podSpec); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					return errors.Wrap(err, "failed to create CSI node pod")
				}
			}
			return nil
		} else {
			return errors.Wrap(err, "failed to get CSI node pod")
		}
	} else {
		return csi.CheckAndDeleteOutdatedCsiNode(ctx, pod, r.Client, targetHash)
	}
}

func (r *containerReconcilerLoop) ManageCsiTopologyLabels(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	csiDriverName := r.getCsiDriverName()
	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return errors.New("node affinity is not set")
	}

	activeMounts, err := r.getCachedActiveMounts(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get active mounts")
	}

	hasActiveMounts := activeMounts != nil && *activeMounts > 0

	csiTopologyLabelsService := operations.NewCsiTopologyLabelsService(csiDriverName, string(nodeName), r.container, hasActiveMounts)
	if !csiTopologyLabelsService.NodeHasExpectedCsiTopologyLabels(r.node) {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "UpdateNodeCsiTopologyLabels")
		defer end()

		expectedLabels := csiTopologyLabelsService.GetExpectedCsiTopologyLabels()
		logger.Info("Updating node with CSI topology labels", "labels", expectedLabels)

		node := csiTopologyLabelsService.UpdateNodeLabels(r.node, expectedLabels)

		err = r.Update(ctx, node)
		if err != nil {
			return errors.Wrap(err, "failed to update node with CSI topology labels")
		}
	}

	return nil
}

func (r *containerReconcilerLoop) CleanupCsiNodeServerPod(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupCsiNodeServerPod")
	defer end()

	namespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}
	csiNodeName := GetCsiNodeServerPodName(r.container.Name)
	if err = r.Delete(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiNodeName,
			Namespace: namespace,
		},
	}); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete CSI node pod", "pod_name", csiNodeName, "namespace", namespace)
			return errors.Wrap(err, "failed to delete CSI node pod")
		}
	}

	logger.Info("CSI node pod deleted", "pod_name", csiNodeName, "namespace", namespace)
	return nil
}

func (r *containerReconcilerLoop) UnsetCsiNodeTopologyLabels(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "UnsetCsiNodeTopologyLabels")
	defer end()

	csiDriverName := r.getCsiDriverName()
	nodeName := r.node.Name

	logger.Info("Unsetting CSI node topology labels", "node", r.node.Name, "csiDriverName", csiDriverName)

	csiTopologyLabelsService := operations.NewCsiTopologyLabelsService(csiDriverName, nodeName, r.container, false)
	node := csiTopologyLabelsService.UpdateNodeLabels(r.node, nil)

	err := r.Update(ctx, node)
	if err != nil {
		return errors.Wrap(err, "failed to update node to unset CSI topology labels")
	}

	return nil
}

func (r *containerReconcilerLoop) shouldDeployCsiNodeServerPod() bool {
	// or we have active mounts
	wekaClientIsRunning := r.wekaClient != nil && r.wekaClient.Status.Status == weka.WekaClientStatusRunning
	preCalculatedActiveMounts := r.activeMounts
	return wekaClientIsRunning || (preCalculatedActiveMounts != nil && *preCalculatedActiveMounts > 0)
}

type csiNodeHashableSpec struct {
	CsiDriverName         string
	CsiRegisterImage      string
	CsiLivenessProbeImage string
	CsiImage              string
	Labels                *util.HashableMap
	Tolerations           []corev1.Toleration
	EnforceTrustedHttps   bool
}

func (r *containerReconcilerLoop) calculateCSINodeHash(enforceTrustedHttps bool, labels map[string]string, tolerations []v1.Toleration) (string, error) {
	spec := csiNodeHashableSpec{
		CsiDriverName:         r.getCsiDriverName(),
		CsiRegisterImage:      config.Config.Csi.RegistrarImage,
		CsiLivenessProbeImage: config.Config.Csi.LivenessProbeImage,
		CsiImage:              config.Config.Csi.WekafsImage,
		Labels:                util.NewHashableMap(labels),
		Tolerations:           tolerations,
		EnforceTrustedHttps:   enforceTrustedHttps,
	}

	return util.HashStruct(spec)
}

func expandCsiNoExecuteTolerations(tolerations []corev1.Toleration) []corev1.Toleration {
	noExecuteTolerations := []string{
		"node.kubernetes.io/disk-pressure",
		"node.kubernetes.io/memory-pressure",
		"node.kubernetes.io/network-unavailable",
		"node.kubernetes.io/cpu-pressure",
		"node.kubernetes.io/unschedulable",
		"node.kubernetes.io/not-ready",
		"node.kubernetes.io/unreachable",
	}
	existingNoExecuteTolerations := map[string]struct{}{}
	for _, t := range tolerations {
		if t.Effect == corev1.TaintEffectNoExecute && slices.Contains(noExecuteTolerations, t.Key) {
			existingNoExecuteTolerations[t.Key] = struct{}{}
		}
	}
	for _, key := range noExecuteTolerations {
		if _, exists := existingNoExecuteTolerations[key]; !exists {
			tolerations = append(tolerations, corev1.Toleration{
				Key:      key,
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			})
		}
	}
	return tolerations
}
