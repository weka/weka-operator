package services

import (
	"context"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KubeService interface {
	GetNode(ctx context.Context, nodeName string) (*v1.Node, error)
}

func NewKubeService(client client.Client) KubeService {
	return &ApiKubeService{
		Client: client,
	}
}

type ApiKubeService struct {
	Client client.Client
}

func (a ApiKubeService) GetNode(ctx context.Context, nodeName string) (*v1.Node, error) {
	node := &v1.Node{}
	err := a.Client.Get(ctx, client.ObjectKey{
		Name: nodeName,
	}, node)
	if err != nil {
		return nil, err
	}
	return node, nil
}
