package e2e

import (
	"context"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type SystemTest struct {
	client.Client
	Ctx             context.Context
	Cfg             *rest.Config
	Namespace       string
	SystemNamespace string
	ClusterName     string
	Image           string

	environment *envtest.Environment
}
