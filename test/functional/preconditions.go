package e2e

import (
	"os"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Precondition struct {
	SystemTest
}

func (p *Precondition) Run(t *testing.T) {
	t.Run("Is Debug", p.IsDebug)
	t.Run("Ping", p.Ping)
	t.Run("Backend Nodes", p.BackendNodes)
	t.Run("Builder Node", p.BuilderNode)
}

func (st *Precondition) IsDebug(t *testing.T) {
	ctx := st.Ctx
	_, _, done := instrumentation.GetLogSpan(ctx, "IsDebug")
	defer done()

	debug := os.Getenv("DEBUG")
	// Debug should either be true or undefined
	if debug != "true" && debug != "" {
		t.Fatalf("Expected DEBUG=true or undefined, got %s", debug)
	}
}

// Ping the cluster by listing the nodes
func (st *Precondition) Ping(t *testing.T) {
	ctx := st.Ctx
	ctx, _, done := instrumentation.GetLogSpan(ctx, "Ping")
	defer done()

	nodes := &v1.NodeList{}
	if err := st.List(ctx, nodes); err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}
	if len(nodes.Items) != 6 {
		t.Fatalf("expected 6 nodes, got %d", len(nodes.Items))
	}
}

// BackendNodes confirms that the backend nodes are present
func (st *Precondition) BackendNodes(t *testing.T) {
	ctx := st.Ctx
	ctx, _, done := instrumentation.GetLogSpan(ctx, "BackendNodes")
	defer done()

	nodes := &v1.NodeList{}
	t.Run("Refresh Backend Nodes", func(t *testing.T) {
		if err := st.List(ctx, nodes, client.MatchingLabels{"weka.io/role": "backend"}); err != nil {
			t.Fatalf("failed to list backend nodes: %v", err)
		}
		if len(nodes.Items) != 5 {
			t.Fatalf("expected 5 backend nodes, got %d", len(nodes.Items))
		}
	})

	t.Run("Verify Nodes Ready", func(t *testing.T) {
		for _, node := range nodes.Items {
			_, logger, done := instrumentation.GetLogSpan(ctx, "Verify Nodes Ready")
			defer done()

			logger.SetValues("node", node.Name)

			for _, condition := range node.Status.Conditions {
				if condition.Type == v1.NodeReady {
					if condition.Status != v1.ConditionTrue {
						logger.Info("Node is not ready", "condition", condition)
						t.Fatalf("node %s is not ready", node.Name)
					}
				}
			}
		}
	})
}

// BuilderNode confirms that the builder node is present
func (st *Precondition) BuilderNode(t *testing.T) {
	ctx := st.Ctx
	ctx, _, done := instrumentation.GetLogSpan(ctx, "BuilderNode")
	defer done()

	nodes := &v1.NodeList{}
	if err := st.List(ctx, nodes, client.MatchingLabels{"weka.io/role": "builder"}); err != nil {
		t.Fatalf("failed to list builder nodes: %v", err)
	}
	if len(nodes.Items) != 1 {
		t.Fatalf("expected 1 builder node, got %d", len(nodes.Items))
	}
}
