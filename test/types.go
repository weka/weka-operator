package test

import (
	"context"

	"github.com/weka/weka-operator/test/e2e/fixtures"
	"github.com/weka/weka-operator/test/e2e/services"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type E2ETest struct {
	Ctx      context.Context
	Jobless  services.Jobless
	Clusters []*ClusterTest
}

type ClusterTest struct {
	Ctx     context.Context
	Jobless services.Jobless

	Image   string
	Cluster *fixtures.Cluster
}

func (ct *ClusterTest) k8sClient(ctx context.Context) (client.Client, error) {
	client, err := ct.Cluster.Kubernetes.GetClient(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (st *ClusterTest) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	client, err := st.k8sClient(ctx)
	if err != nil {
		return err
	}
	return client.Get(ctx, key, obj)
}

func (st *ClusterTest) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	client, err := st.k8sClient(ctx)
	if err != nil {
		return err
	}
	return client.Create(ctx, obj, opts...)
}

func (st *ClusterTest) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	client, err := st.k8sClient(ctx)
	if err != nil {
		return err
	}
	return client.List(ctx, obj, opts...)
}
