// Command weka-capacity is a dry-run CLI for the weka-operator capacity planner. It reproduces the exact
// inputs the operator uses — the shared inventory collector (internal/capacityplanner/inventory) and the
// pure planner (internal/capacityplanner) — so operators can preview, offline, what drive/compute
// containers a clusterCapacity change would create or grow, on which nodes, and whether it is feasible.
//
// Two subcommands:
//   - explore-nodes : per-node capacity / resource headroom and the WekaContainers consuming each node.
//   - plan          : dry-run the planner for a WekaCluster (live spec + flag overrides) and show the
//     create/grow/compute sets, feasibility, and fix tips.
//
// It ships in the operator image and runs either locally (KUBECONFIG) or in-cluster (kubectl exec).
package main

import (
	"fmt"
	"os"

	flags "github.com/jessevdk/go-flags"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// options are the global flags shared by all subcommands, plus the subcommand set.
type options struct {
	Kubeconfig        string `long:"kubeconfig" env:"KUBECONFIG" description:"Path to kubeconfig; empty uses in-cluster config"`
	Namespace         string `long:"namespace" short:"n" default:"weka-operator-system" description:"Cluster namespace: the WekaCluster lookup namespace for 'plan' and the node/selector namespace context"`
	OperatorNamespace string `long:"operator-namespace" default:"weka-operator-system" description:"Namespace to scrape the operator manager Deployment's env from (constraint base)"`
	Output            string `long:"output" choice:"table" choice:"json" default:"table" description:"Output format"`
	Out               string `long:"out" description:"Write output to this file instead of stdout"`

	ExploreNodes exploreNodesCommand `command:"explore-nodes" description:"Show per-node capacity/resource headroom and consumers"`
	Plan         planCommand         `command:"plan" description:"Dry-run the capacity planner for a cluster (create/grow/compute/feasibility)"`
}

var opts options

func main() {
	parser := flags.NewParser(&opts, flags.Default)
	if _, err := parser.Parse(); err != nil {
		if fe, ok := err.(*flags.Error); ok && fe.Type == flags.ErrHelp {
			os.Exit(0)
		}
		// A command's Execute may signal "infeasible" for scriptability; it prints its own output.
		if _, ok := err.(*flags.Error); !ok {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

// newClient builds a controller-runtime client from --kubeconfig (or in-cluster) with the operator scheme
// (client-go core types + weka v1alpha1), matching what the operator registers so plans never diverge.
func newClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := weka.AddToScheme(scheme); err != nil {
		return nil, err
	}
	var (
		cfg *rest.Config
		err error
	)
	if opts.Kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", opts.Kubeconfig)
	}
	if err != nil {
		return nil, fmt.Errorf("building kube config: %w", err)
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

// writeOutput writes s to --out (if set) or stdout.
func writeOutput(s string) error {
	if opts.Out == "" {
		fmt.Print(s)
		return nil
	}
	if err := os.WriteFile(opts.Out, []byte(s), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", opts.Out, err)
	}
	return nil
}
