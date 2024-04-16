package controllers

import (
	"context"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
)

func GetCluster(ctx context.Context, c client.Client, name v1alpha1.ObjectReference) (*v1alpha1.WekaCluster, error) {
	cluster := &v1alpha1.WekaCluster{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: name.Namespace,
		Name:      name.Name,
	}, cluster)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get cluster")
	}
	return cluster, nil
}

func GetJoinIps(ctx context.Context, c client.Client, cluster *v1alpha1.WekaCluster) ([]string, error) {
	containers := GetClusterContainers(ctx, c, cluster, "compute")
	if len(containers) == 0 {
		return nil, errors.New("No compute containers found")
	}

	joinIpPortPairs := []string{}
	for _, container := range containers {
		joinIpPortPairs = append(joinIpPortPairs, container.Status.ManagementIP+":"+strconv.Itoa(container.Spec.Port))
	}
	return joinIpPortPairs, nil
}

func GetClusterContainers(ctx context.Context, c client.Client, cluster *v1alpha1.WekaCluster, mode string) []*v1alpha1.WekaContainer {
	// Get all containers in the cluster, filtering
	containersList := v1alpha1.WekaContainerList{}
	listOpts := []client.ListOption{
		//client.InNamespace(cluster.Namespace),
		//client.MatchingFields{"metadata.ownerReferences.uid": string(cluster.UID)},
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}
	err := c.List(ctx, &containersList, listOpts...)

	if err != nil {
		return nil
	}

	containers := []*v1alpha1.WekaContainer{}
	for i := range containersList.Items {
		containers = append(containers, &containersList.Items[i])
	}
	return containers
}
