package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// constraintFlags are the per-helm-constraint overrides shared by both subcommands. Each is a POINTER:
// nil = flag not set = keep the scraped/base value (never a re-typed literal default). MinChunkSizeGiB is
// intentionally absent — it is a compile-time constant, surfaced in output but not overridable.
type constraintFlags struct {
	FromOperator         string   `long:"from-operator" choice:"true" choice:"false" default:"true" description:"Scrape the deployed operator's env for the base constraints. Pass --from-operator=false (or --from-operator false) to use built-in env defaults only."`
	TlcPerCoreGiB        *int     `long:"tlc-per-core-gib" description:"Override TLC capacity per drive core (GiB)"`
	QlcPerCoreGiB        *int     `long:"qlc-per-core-gib" description:"Override QLC capacity per drive core (GiB)"`
	ImbalanceFactor      *float64 `long:"imbalance-factor" description:"Override the heterogeneous-growth imbalance factor"`
	DeadbandFraction     *float64 `long:"deadband-fraction" description:"Override the capacity shortfall deadband fraction"`
	MaxComputeCores      *int     `long:"max-compute-cores-per-node" description:"Override the per-container compute-core cap"`
	MinGrowthFraction    *float64 `long:"min-growth-fraction" description:"Override the minimum in-place grow fraction"`
	MaxOverprovision     *float64 `long:"max-overprovision-fraction" description:"Override the max over-provision fraction"`
	EnableDynamicScaling *bool    `long:"enable-dynamic-drive-scaling" description:"Override enableDynamicDriveScalingForSharedDrives (in-place growth)"`
	AllowSingleParity    *bool    `long:"allow-single-parity" description:"Override the single-parity protection-floor relaxation"`
	HugepagesTlcRatio    *int     `long:"hugepages-tlc-ratio" description:"Override the compute-hugepages TLC ratio"`
	HugepagesQlcRatio    *int     `long:"hugepages-qlc-ratio" description:"Override the compute-hugepages QLC ratio"`
}

// loadConstraints builds the capacity constraints in three layers — NEVER re-hardcoding a value the
// operator reads from env:
//  1. Base: globalconfig.LoadCapacityEnv() (env.go's single source of the built-in defaults).
//  2. Overlay: scrape the deployed operator Deployment's env into the process, so LoadCapacityEnv picks
//     up the operator's actual values (skipped with --from-operator=false).
//  3. Flag overrides (this struct), applied last.
//
// The DPDK per-core fields are NOT set here — the plan command fills them from the cluster spec (per-role),
// exactly as the controller does; explore-nodes leaves them at zero (it charges existing containers by
// their own capacity, which already includes their DPDK reservation via the shared sizing model).
func loadConstraints(ctx context.Context, c client.Client, namespace string, f *constraintFlags) (*allocator.CapacityConstraints, error) {
	if scrapeEnabled(f) {
		envMap, err := scrapeOperatorEnv(ctx, c, namespace)
		if err != nil {
			return nil, err
		}
		for k, v := range envMap {
			os.Setenv(k, v) //nolint:errcheck // overlay into the process so LoadCapacityEnv reads the operator's values; os.Setenv cannot fail here
		}
	}
	globalconfig.LoadCapacityEnv()
	cons := allocator.CapacityConstraintsFromConfig()
	applyConstraintOverrides(cons, f) // flag overrides, top layer
	return cons, nil
}

// scrapeEnabled reports whether the deployed-operator env scrape (layer 2) should run. The scrape is ON by
// default; only an explicit --from-operator=false (or --from-operator false) disables it. An empty value —
// e.g. a zero-valued constraintFlags literal in a unit test — is treated as "on", matching the flag default.
func scrapeEnabled(f *constraintFlags) bool {
	return f.FromOperator != "false"
}

// applyConstraintOverrides applies the set (non-nil) flag overrides onto cons — the top layer of the
// three-layer constraint loading. Kept separate so the override precedence is unit-testable without a
// Kubernetes client.
func applyConstraintOverrides(cons *allocator.CapacityConstraints, f *constraintFlags) {
	if f.TlcPerCoreGiB != nil {
		cons.TlcCapacityPerCoreGiB = *f.TlcPerCoreGiB
	}
	if f.QlcPerCoreGiB != nil {
		cons.QlcCapacityPerCoreGiB = *f.QlcPerCoreGiB
	}
	if f.ImbalanceFactor != nil {
		cons.ImbalanceFactor = *f.ImbalanceFactor
	}
	if f.DeadbandFraction != nil {
		cons.CapacityDeadbandFraction = *f.DeadbandFraction
	}
	if f.MaxComputeCores != nil {
		cons.MaxComputeCoresPerNode = *f.MaxComputeCores
	}
	if f.MinGrowthFraction != nil {
		cons.MinGrowthFraction = *f.MinGrowthFraction
	}
	if f.MaxOverprovision != nil {
		cons.MaxOverProvisionFraction = *f.MaxOverprovision
	}
	if f.EnableDynamicScaling != nil {
		cons.AllowInPlaceGrowth = *f.EnableDynamicScaling
	}
	if f.AllowSingleParity != nil {
		cons.AllowSingleParity = *f.AllowSingleParity
	}
	if f.HugepagesTlcRatio != nil {
		cons.ComputeHugepagesTlcRatio = *f.HugepagesTlcRatio
	}
	if f.HugepagesQlcRatio != nil {
		cons.ComputeHugepagesQlcRatio = *f.HugepagesQlcRatio
	}
}

// scrapeOperatorEnv finds the operator's manager Deployment in namespace and returns its container env
// as plain name→value pairs (env sourced from secrets/configmaps via valueFrom is skipped). The container
// is identified by carrying a known capacity env key, so it works regardless of the Deployment's name.
func scrapeOperatorEnv(ctx context.Context, c client.Client, namespace string) (map[string]string, error) {
	var deps appsv1.DeploymentList
	if err := c.List(ctx, &deps, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing deployments in %q: %w", namespace, err)
	}
	for i := range deps.Items {
		for ci := range deps.Items[i].Spec.Template.Spec.Containers {
			ct := &deps.Items[i].Spec.Template.Spec.Containers[ci]
			env := make(map[string]string, len(ct.Env))
			operatorContainer := false
			for _, e := range ct.Env {
				if e.ValueFrom != nil {
					continue
				}
				env[e.Name] = e.Value
				if e.Name == "CLUSTER_CAPACITY_TLC_CAPACITY_PER_CORE_GIB" || e.Name == "VERSION" {
					operatorContainer = true
				}
			}
			if operatorContainer {
				return env, nil
			}
		}
	}
	return nil, fmt.Errorf("no operator manager Deployment with capacity env found in namespace %q "+
		"(pass --from-operator=false to use built-in defaults, or --operator-namespace <operator-namespace>)", namespace)
}

// parseSelector parses a comma-separated "k=v,k2=v2" label selector into a map. An empty string yields an
// empty (match-all) selector.
func parseSelector(s string) (map[string]string, error) {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			return nil, fmt.Errorf("invalid selector term %q (want key=value)", pair)
		}
		out[kv[0]] = kv[1]
	}
	return out, nil
}
