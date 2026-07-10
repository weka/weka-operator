package main

import (
	"context"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
)

// exploreNodesCommand renders the per-node capacity/resource landscape independent of any cluster.
type exploreNodesCommand struct {
	Selector    string          `long:"selector" default:"weka.io/supports-backends=true" description:"Node label selector (k=v[,k=v...]); default selects backend nodes"`
	FDLabel     string          `long:"fd-label" description:"Failure-domain label key (label-based FD mode); default AUTO = FD per host"`
	Detail      string          `long:"detail" description:"Show the WekaContainers consuming this specific node"`
	Constraints constraintFlags `group:"Constraint overrides (used to charge existing containers' footprint)"`
}

func (cmd *exploreNodesCommand) Execute(_ []string) error {
	ctx := context.Background()
	c, err := newClient()
	if err != nil {
		return err
	}
	cons, err := loadConstraints(ctx, c, opts.OperatorNamespace, &cmd.Constraints)
	if err != nil {
		return err
	}
	selector, err := parseSelector(cmd.Selector)
	if err != nil {
		return err
	}
	var fd *weka.FailureDomain
	if cmd.FDLabel != "" {
		lbl := cmd.FDLabel
		fd = &weka.FailureDomain{Label: &lbl}
	}
	nodes, err := inventory.NewCollector(c).ExploreNodes(ctx, selector, fd, cons)
	if err != nil {
		return err
	}
	if opts.Output == "json" {
		s, err := renderNodesJSON(nodes)
		if err != nil {
			return err
		}
		return writeOutput(s)
	}
	return writeOutput(renderNodesTable(nodes, cmd.Detail))
}
