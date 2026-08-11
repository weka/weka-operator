package capacityplanner

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/pkg/util"
)

// cpu.go is the SINGLE SOURCE OF TRUTH for translating a container's weka data cores into the physical
// CPU count its pod actually reserves. It mirrors internal/controllers/resources/pod.go setResources()
// exactly, so the capacity planner's node-CPU headroom gate (and the explore-nodes free-CPU view) charge
// the same physical CPUs the kube-scheduler will. pod.go's dedicated/dedicated_ht branches call
// CPURequestCores; the planner projects FRESH containers (which have no spec yet) via cpuModel.
//
// Why this exists: a weka pod with cpuPolicy=auto resolves to dedicated_ht on a hyper-threaded node,
// whose CPU request is numCores*2+1 (dedicated = numCores+1) — NOT the weka data-core count. Charging
// only data cores against a node's physical Allocatable CPU over-reports headroom ~2x on HT nodes and
// lets the planner green-light CPU-tight plans the scheduler then rejects.

// NodeCPUTopology bundles the two node CPU attributes the request formula depends on, so callers pass one
// value instead of a pair of bare bools. Built once per node from the weka.io/discovery.json annotation
// (DiscoveryNodeInfo.IsHt / NodeFullPcpusOnly), the latter OR'd with config.Config.FullPcpusOnly.
type NodeCPUTopology struct {
	IsHt          bool
	FullPcpusOnly bool
}

// CPURequestCores returns the physical CPU count the pod for spec reserves on a node with topology topo.
// It mirrors internal/controllers/resources/pod.go setResources() so the planner and the real pod stay
// in lockstep. Manual/Shared with an explicit CPU request use that quantity (ceil to whole cores);
// Manual without explicit limits falls back to numCores+1 (pod.go requests 1000*n+100 millicpu, which
// rounds up to n+1). cpuPolicy=auto is resolved from topo.IsHt.
func CPURequestCores(spec *weka.WekaContainerSpec, topo NodeCPUTopology) int {
	totalNumCores := spec.NumCores
	if SupportsExtraCores(spec.Mode) {
		totalNumCores += spec.ExtraCores
	}
	policy := resolveCPUPolicy(spec.CpuPolicy, topo.IsHt)

	switch policy {
	case weka.CpuPolicyDedicatedHT:
		total := totalNumCores*2 + 1
		if spec.Mode == weka.WekaContainerModeEnvoy {
			total = totalNumCores
		} else if SupportsExtraCores(spec.Mode) {
			total -= spec.ExtraCores // ExtraCores is counted once, not doubled (mirrors pod.go)
		}
		return roundFullPcpus(total, topo)
	case weka.CpuPolicyDedicated:
		total := totalNumCores + 1
		if spec.Mode == weka.WekaContainerModeEnvoy {
			total = totalNumCores
		}
		return roundFullPcpus(total, topo)
	case weka.CpuPolicyManual:
		if spec.Resources != nil && !spec.Resources.Limits.Cpu.IsZero() {
			return util.CeilDiv(int(spec.Resources.Requests.Cpu.MilliValue()), 1000)
		}
		return totalNumCores + 1
	case weka.CpuPolicyShared:
		if spec.Resources != nil {
			return util.CeilDiv(int(spec.Resources.Requests.Cpu.MilliValue()), 1000)
		}
		return 0
	}
	return totalNumCores
}

// cpuModel returns the per-data-core physical CPU multiplier and the per-container base (management core)
// the planner charges when placing a FRESH container, where only a data-core count and the role's target
// cpuPolicy exist (no spec). The coefficients are SAMPLED from CPURequestCores — the single CPU-request
// encoding — at 1 and 2 data cores, so this projection cannot drift from the real pod request. The
// fullPcpusOnly odd-core +1 rounding is intentionally NOT modeled here (a ≤1-CPU/odd-container
// approximation the incremental placement can't cheaply re-derive; sampling with it stripped keeps the
// slope linear); it is exact for existing containers via CPURequestCores and fullPcpusOnly is off by
// default.
func cpuModel(policy weka.CpuPolicy, topo NodeCPUTopology) (perCore, base int) {
	t := NodeCPUTopology{IsHt: topo.IsHt}
	spec := weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, CpuPolicy: policy, NumCores: 1}
	c1 := CPURequestCores(&spec, t)
	spec.NumCores = 2
	c2 := CPURequestCores(&spec, t)
	if perCore = c2 - c1; perCore <= 0 {
		return 1, 1 // shared/other without an explicit request: preserve the prior 1:1 projection
	}
	return perCore, c1 - perCore
}

// resolveCPUPolicy resolves cpuPolicy=auto exactly as pod.go setResources() does: HT => dedicated_ht,
// non-HT => dedicated. pod.go ignores coreIds under auto (its HT/non-HT assignment overwrites the
// coreIds-implied manual), so this must too — explicit core pinning requires cpuPolicy=manual. Non-auto
// policies pass through unchanged.
func resolveCPUPolicy(policy weka.CpuPolicy, isHt bool) weka.CpuPolicy {
	if policy != weka.CpuPolicyAuto && policy != "" {
		return policy
	}
	if isHt {
		return weka.CpuPolicyDedicatedHT
	}
	return weka.CpuPolicyDedicated
}

// IsDataCoreMode reports whether the mode runs weka data cores. It is NOT the ExtraCores gate — client
// takes ExtraCores too without being a data-core mode; see SupportsExtraCores, currently its only caller.
func IsDataCoreMode(mode string) bool {
	switch mode {
	case weka.WekaContainerModeCompute, weka.WekaContainerModeDrive, weka.WekaContainerModeS3,
		weka.WekaContainerModeNfs, weka.WekaContainerModeSmbw, weka.WekaContainerModeDataServices:
		return true
	}
	return false
}

// SupportsExtraCores reports whether the mode folds ExtraCores into its pod CPU request.
// Data-core modes plus client: for client, ExtraCores buys cpuset headroom for the
// weka-aio-* / management threads, which the runtime auto-classifies as non-datapath cores.
func SupportsExtraCores(mode string) bool {
	return IsDataCoreMode(mode) || mode == weka.WekaContainerModeClient
}

// roundFullPcpus applies pod.go's SMT alignment: on an HT node with full-pcpus-only, an odd CPU count is
// bumped to the next even number so a pod never straddles a physical core's two threads.
func roundFullPcpus(total int, topo NodeCPUTopology) int {
	if topo.IsHt && topo.FullPcpusOnly && total%2 != 0 {
		return total + 1
	}
	return total
}
