package wekacluster

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/factory"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/pkg/util"
)

// daemonset.go sizes a cluster with no counts or capacity (Spec.Dynamic.UsesAutoFullDrives): one drive and
// one compute container per matching node. A pod that does not fit its node stays Pending.

type dsNode struct {
	driveRole, computeRole bool   // matches the role's node selector
	ineligible             string // "" or the reason new containers are withheld
	drivesGiB              []int  // non-blocked full drives from the annotation, largest first
	badAnnotation          string // parse error of the drive annotation, if any
	entries                []domain.DriveEntry
	// listed: availGiB is set. availGiB = drives our container holds + free ones, largest first.
	listed   bool
	availGiB []int
}

type daemonsetInput struct {
	nodes map[string]dsNode
	// Own containers keyed by mode, then node. A deleting container is only recorded as deleting, so its
	// node is neither counted nor re-filled.
	live                                         map[string]map[string]*weka.WekaContainer
	deleting                                     map[string]map[string]bool
	numDrivesPin, driveCoresPin, computeCoresPin int
	hugepagesPin, hugepagesOffsetPin             map[string]bool // by mode
	cons                                         *capacityplanner.CapacityConstraints
}

type dsContainerTarget struct {
	node, mode string
	paired     bool // the node has both a drive and a compute target
	numDrives  int  // drive only
	// drivesFollowFree (drive only): numDrives tracks what the node can give this container (drives it holds plus
	// the free ones), so it is written as is and may go up or down. Otherwise it only ratchets up. Set for an
	// unpinned own container on a node whose drive containers were listed this pass and whose allocated serials
	// are all still signed and unblocked; a pin is never lowered.
	drivesFollowFree    bool
	cores               int
	tlcGiB              int // drive only: capacity of the numDrives largest drives
	computeHugepagesMiB int // compute only
}

type daemonsetPlan struct {
	targets []dsContainerTarget // drive then compute, each by node
	// Inputs for the aggregated events.
	unsigned, unused, capped, noFree []string
	ineligible                       map[string]string // node -> reason
	driveCores, computeCores         int
	// Printer columns only; placement never reads them.
	statusDesiredDrive, statusDesiredCompute int
}

func planDaemonset(in *daemonsetInput) daemonsetPlan {
	cons := in.cons
	p := daemonsetPlan{ineligible: map[string]string{}}
	liveDrive, liveCompute := in.live[weka.WekaContainerModeDrive], in.live[weka.WekaContainerModeCompute]
	// Containers on nodes outside both selectors still count, and grow only from pins and fleet-wide compute
	// sizing: their node's annotation is not read.
	all := map[string]bool{}
	for name := range in.nodes {
		all[name] = true
	}
	for _, m := range []map[string]*weka.WekaContainer{liveDrive, liveCompute} {
		for name := range m {
			all[name] = true
		}
	}
	names := slices.Sorted(maps.Keys(all))

	driveT := map[string]dsContainerTarget{}
	var totalCores, totalTlc int
	for _, name := range names {
		n := in.nodes[name]
		var exDrives, exCores int
		has := liveDrive[name] != nil
		if has {
			exDrives, exCores = liveDrive[name].Spec.NumDrives, liveDrive[name].Spec.NumCores
		}
		if in.deleting[weka.WekaContainerModeDrive][name] && !has {
			continue
		}
		if !has {
			if !n.driveRole {
				continue
			}
			if len(n.drivesGiB) == 0 {
				frag := name
				if n.badAnnotation != "" {
					frag += " (unparsable annotation: " + n.badAnnotation + ")"
				}
				p.unsigned = append(p.unsigned, frag)
				continue
			}
			if n.ineligible != "" {
				p.ineligible[name] = n.ineligible
				continue
			}
		}
		pool := n.drivesGiB
		if n.listed {
			pool = n.availGiB
		}
		drives := max(exDrives, in.numDrivesPin)
		var heldCaps []int
		holdsBlocked := false
		if has {
			heldCaps, holdsBlocked = heldCapacities(liveDrive[name], n.entries)
		}
		followFree := has && n.listed && in.numDrivesPin == 0 && !holdsBlocked
		switch {
		case !has:
			drives = cmp.Or(in.numDrivesPin, len(pool))
			if drives == 0 {
				p.noFree = append(p.noFree, fmt.Sprintf("%s (%d signed, %d held by other containers)", name, len(n.drivesGiB), len(n.drivesGiB)-len(pool)))
				continue
			}
		case followFree:
			drives = max(len(pool), 1)
			if len(pool) == 0 {
				p.noFree = append(p.noFree, fmt.Sprintf("%s (%d signed, %d held by other containers; container waits for a free drive)", name, len(n.drivesGiB), len(n.drivesGiB)))
			}
		}
		if in.numDrivesPin > 0 && drives < len(n.drivesGiB) {
			p.unused = append(p.unused, fmt.Sprintf("%d of %d signed drives unused on %s (numDrives pin=%d)", len(n.drivesGiB)-drives, len(n.drivesGiB), name, in.numDrivesPin))
		}
		cores := cmp.Or(in.driveCoresPin, capacityplanner.FullDriveCores(drives, cons))
		t := dsContainerTarget{node: name, mode: weka.WekaContainerModeDrive, numDrives: drives, drivesFollowFree: followFree, cores: max(exCores, cores)}
		// An existing container's capacity is what it holds plus the largest free drives for the rest, so it is
		// the same whether or not the node was listed this pass.
		free := pool
		if has {
			free = withoutCapacities(pool, heldCaps)
			t.tlcGiB = sum(heldCaps)
			drives = max(drives-len(heldCaps), 0)
		}
		t.tlcGiB += sum(free[:min(drives, len(free))])
		driveT[name] = t
		totalCores += t.cores
		totalTlc += t.tlcGiB
	}

	capAt := func(x int) int {
		if m := cons.MaxCoresPerContainer; m > 0 && x > m {
			return m
		}
		return x
	}

	computeT := map[string]dsContainerTarget{}
	var only []string
	var pairedCores, pairedTlc int
	for _, name := range names {
		n := in.nodes[name]
		var exCores int
		has := liveCompute[name] != nil
		if has {
			exCores = liveCompute[name].Spec.NumCores
		}
		if in.deleting[weka.WekaContainerModeCompute][name] && !has {
			continue
		}
		d, hasDrive := driveT[name]
		if !has {
			switch {
			case !n.computeRole:
				continue
			case n.ineligible != "":
				p.ineligible[name] = n.ineligible
				continue
			case n.driveRole && (!hasDrive || len(n.drivesGiB) == 0):
				continue
			}
		}
		if !hasDrive {
			only = append(only, name)
			continue
		}
		cores := in.computeCoresPin
		if cores == 0 {
			want := max(capacityplanner.RequiredComputeCores(d.cores, 0, true, cons), 1)
			cores = capAt(want)
			if max(exCores, cores) < want {
				p.capped = append(p.capped, fmt.Sprintf("%s (%d cores wanted)", name, want))
			}
		}
		cores = max(exCores, cores)
		computeT[name] = dsContainerTarget{
			node: name, mode: weka.WekaContainerModeCompute, paired: true, cores: cores,
			computeHugepagesMiB: capacityplanner.ComputeContainerHugepagesMiB(d.tlcGiB, 0, 1, cores, cons),
		}
		pairedCores += cores
		pairedTlc += d.tlcGiB
	}
	p.driveCores, p.computeCores = totalCores, pairedCores
	if m := len(only); m > 0 {
		rem := capacityplanner.RequiredComputeCores(totalCores, 0, true, cons) - pairedCores
		cores, want := in.computeCoresPin, 0
		if cores == 0 {
			want = max((rem+m-1)/m, 1)
			cores = capAt(want)
		}
		remTlc := max(totalTlc-pairedTlc, 0)
		capped := 0
		for _, name := range only {
			c := cores
			if lc := liveCompute[name]; lc != nil {
				c = max(lc.Spec.NumCores, cores)
			}
			if c < want {
				capped++
			}
			computeT[name] = dsContainerTarget{
				node: name, mode: weka.WekaContainerModeCompute, cores: c,
				computeHugepagesMiB: capacityplanner.ComputeContainerHugepagesMiB(remTlc, 0, m, c, cons),
			}
			p.computeCores += c
		}
		if capped > 0 {
			p.capped = append(p.capped, fmt.Sprintf("%d compute-only node(s) (%d cores wanted)", capped, want))
		}
	}

	for _, name := range names {
		if t, ok := driveT[name]; ok {
			_, t.paired = computeT[name]
			p.targets = append(p.targets, t)
		}
	}
	for _, name := range names {
		if t, ok := computeT[name]; ok {
			p.targets = append(p.targets, t)
		}
	}

	for _, name := range names {
		n := in.nodes[name]
		if liveDrive[name] != nil || (n.driveRole && n.ineligible == "") {
			p.statusDesiredDrive++
		}
		if liveCompute[name] != nil || (n.computeRole && n.ineligible == "") {
			p.statusDesiredCompute++
		}
	}
	return p
}

func sum(xs []int) (total int) {
	for _, x := range xs {
		total += x
	}
	return total
}

// heldCapacities returns the capacities of c's allocated serials that are still signed and unblocked on the
// node, and whether any allocated serial is not.
func heldCapacities(c *weka.WekaContainer, entries []domain.DriveEntry) (caps []int, missing bool) {
	if c.Status.Allocations == nil {
		return nil, false
	}
	for _, serial := range c.Status.Allocations.Drives {
		i := slices.IndexFunc(entries, func(d domain.DriveEntry) bool { return d.Serial == serial })
		if i < 0 {
			missing = true
			continue
		}
		caps = append(caps, entries[i].CapacityGiB)
	}
	return caps, missing
}

// withoutCapacities removes one occurrence of each of remove from pool, keeping its order.
func withoutCapacities(pool, remove []int) []int {
	out := slices.Clone(pool)
	for _, g := range remove {
		if i := slices.Index(out, g); i >= 0 {
			out = slices.Delete(out, i, i+1)
		}
	}
	return out
}

// olderThan orders containers by creation time, then by name when the timestamps tie.
func olderThan(a, b *weka.WekaContainer) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// containersByMode indexes this cluster's drive and compute containers by mode, then node.
func (r *wekaClusterReconcilerLoop) containersByMode() (live map[string]map[string]*weka.WekaContainer, deleting map[string]map[string]bool) {
	live, deleting = map[string]map[string]*weka.WekaContainer{}, map[string]map[string]bool{}
	for _, mode := range []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute} {
		live[mode], deleting[mode] = map[string]*weka.WekaContainer{}, map[string]bool{}
	}
	for _, c := range r.containers {
		node := string(c.GetNodeAffinity())
		if live[c.Spec.Mode] == nil || node == "" {
			continue
		}
		if c.IsMarkedForDeletion() || c.IsDeletingState() || c.IsDestroyingState() {
			deleting[c.Spec.Mode][node] = true
			continue
		}
		live[c.Spec.Mode][node] = c
	}
	return live, deleting
}

func (r *wekaClusterReconcilerLoop) collectDaemonsetInput(ctx context.Context) (*daemonsetInput, error) {
	cluster := r.cluster
	dyn := cmp.Or(cluster.Spec.Dynamic, &weka.WekaClusterTemplate{})
	in := &daemonsetInput{
		numDrivesPin: dyn.NumDrives, driveCoresPin: dyn.DriveCores, computeCoresPin: dyn.ComputeCores,
		hugepagesPin:       map[string]bool{weka.WekaContainerModeDrive: dyn.DriveHugepages > 0, weka.WekaContainerModeCompute: dyn.ComputeHugepages > 0},
		hugepagesOffsetPin: map[string]bool{weka.WekaContainerModeDrive: dyn.DriveHugepagesOffset > 0, weka.WekaContainerModeCompute: dyn.ComputeHugepagesOffset > 0},
		cons:               allocator.ConstraintsForClusterSpec(&cluster.Spec),
		nodes:              map[string]dsNode{},
	}
	in.live, in.deleting = r.containersByMode()

	tolerations := resources.GetWekaPodTolerationsForCluster(cluster)
	add := func(list []v1.Node, role string) {
		for i := range list {
			node := &list[i]
			n, seen := in.nodes[node.Name]
			if !seen {
				n.ineligible = resources.NodeIneligibleReason(node, tolerations)
				if n.ineligible == "" && cluster.Spec.FailureDomain != nil && allocator.ResolveNodeFDValue(node, cluster.Spec.FailureDomain) == "" {
					n.ineligible = "missing FD label"
				}
				entries, _, err := allocator.SignedFullDrives(node)
				if err != nil {
					n.badAnnotation = err.Error()
				}
				n.entries = entries
				for _, d := range entries {
					n.drivesGiB = append(n.drivesGiB, d.CapacityGiB)
				}
			}
			if role == weka.WekaContainerModeDrive {
				n.driveRole = true
			} else {
				n.computeRole = true
			}
			in.nodes[node.Name] = n
		}
	}
	driveSel, computeSel := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive), cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)
	driveNodes, err := inventory.ListNodesForSelector(ctx, r.getClient(), driveSel)
	if err != nil {
		return in, fmt.Errorf("collectDaemonsetInput: failed to list drive nodes: %w", err)
	}
	add(driveNodes, weka.WekaContainerModeDrive)
	computeNodes := driveNodes
	if !maps.Equal(driveSel, computeSel) {
		if computeNodes, err = inventory.ListNodesForSelector(ctx, r.getClient(), computeSel); err != nil {
			return in, fmt.Errorf("collectDaemonsetInput: failed to list compute nodes: %w", err)
		}
	}
	add(computeNodes, weka.WekaContainerModeCompute)
	if err := r.listFreeDrives(ctx, in); err != nil {
		return in, err
	}
	return in, nil
}

// listFreeDrives sets availGiB on the nodes where this cluster may take or lose drives to other containers:
// no drive container yet, or one that holds nothing, fewer than its spec asks for, or fewer than the node's
// signed drives. A node where it holds every signed drive is never listed, and with no such node there is no List.
func (r *wekaClusterReconcilerLoop) listFreeDrives(ctx context.Context, in *daemonsetInput) error {
	liveDrive := in.live[weka.WekaContainerModeDrive]
	var candidates []string
	for name, n := range in.nodes {
		c := liveDrive[name]
		switch {
		case len(n.entries) == 0:
		case c == nil:
			if n.driveRole && n.ineligible == "" && !in.deleting[weka.WekaContainerModeDrive][name] {
				candidates = append(candidates, name)
			}
		default:
			held := 0
			if c.Status.Allocations != nil {
				held = len(c.Status.Allocations.Drives)
			}
			if held == 0 || c.Spec.NumDrives > held || held < len(n.entries) {
				candidates = append(candidates, name)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	list := &weka.WekaContainerList{}
	if err := r.getClient().List(ctx, list, client.MatchingLabels{domain.WekaLabelMode: weka.WekaContainerModeDrive}, client.UnsafeDisableDeepCopy); err != nil {
		return fmt.Errorf("listFreeDrives: failed to list drive containers: %w", err)
	}
	held, own := map[string]bool{}, map[string]bool{}
	reserved := map[string]int{}
	ownKeys := map[string]bool{}
	for _, c := range r.containers {
		ownKeys[c.Namespace+"/"+c.Name] = true
	}
	for i := range list.Items {
		c := &list.Items[i]
		node := string(c.GetNodeAffinity())
		if node == "" || c.UsesDriveSharing() {
			continue
		}
		var serials []string
		if c.Status.Allocations != nil {
			serials = c.Status.Allocations.Drives
		}
		target := held
		if lc := liveDrive[node]; lc != nil && lc.Namespace == c.Namespace && lc.Name == c.Name {
			target = own
		}
		for _, s := range serials {
			target[s] = true
		}
		if ownKeys[c.Namespace+"/"+c.Name] || c.IsMarkedForDeletion() || c.IsDeletingState() || c.IsDestroyingState() {
			continue
		}
		// The older claim wins: a foreign container created after ours does not reserve against it.
		if lc := liveDrive[node]; lc != nil && !olderThan(c, lc) {
			continue
		}
		reserved[node] += max(c.Spec.NumDrives-len(serials), 0)
	}
	for _, name := range candidates {
		n := in.nodes[name]
		var mine, free []int
		for _, d := range n.entries {
			switch {
			case own[d.Serial]:
				mine = append(mine, d.CapacityGiB)
			case !held[d.Serial]:
				free = append(free, d.CapacityGiB)
			}
		}
		// The allocator gives the largest free drives first, so a container that has not claimed yet takes those.
		free = free[min(reserved[name], len(free)):]
		n.availGiB = capacityplanner.SortDriveCapacitiesDesc(append(mine, free...))
		n.listed = true
		in.nodes[name] = n
	}
	return nil
}

// buildDaemonsetContainers raises existing containers to the plan, then builds the missing ones. Before the
// cluster forms, creation is capped per role at the form-cluster maximum, nodes holding both roles first.
func (r *wekaClusterReconcilerLoop) buildDaemonsetContainers(ctx context.Context) (built []*weka.WekaContainer, computeCount int, skipped []string, err error) {
	cluster := r.cluster
	in, err := r.collectDaemonsetInput(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	p := planDaemonset(in)
	r.emitDaemonsetEvents(&p, in.cons.FullDrivesComputeToDriveCoreRatio)
	live := in.live

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &weka.ClusterMetrics{}
	}
	var targets allocator.IntPerWekaRole
	var desiredDrives int
	for _, t := range p.targets {
		desiredDrives += t.numDrives
		if t.mode == weka.WekaContainerModeDrive {
			targets.Drive++
		} else {
			targets.Compute++
		}
	}
	computeCount = targets.Compute
	cluster.Status.Stats.Containers.Drive.Containers.Desired = weka.IntMetric(p.statusDesiredDrive)
	cluster.Status.Stats.Containers.Compute.Containers.Desired = weka.IntMetric(p.statusDesiredCompute)
	cluster.Status.Stats.Drives.DriveCounters.Desired = weka.IntMetric(desiredDrives)

	var grown, belowAuto []string
	var growErrs []error
	var missing []dsContainerTarget
	for _, t := range p.targets {
		c := live[t.mode][t.node]
		if c == nil {
			missing = append(missing, t)
			continue
		}
		summary, below, growErr := r.raiseDaemonsetContainer(ctx, c, &t, in.hugepagesPin[t.mode], in.hugepagesOffsetPin[t.mode])
		belowAuto = append(belowAuto, below...)
		if growErr != nil {
			growErrs = append(growErrs, growErr)
		} else if summary != "" {
			grown = append(grown, summary)
		}
	}
	if len(grown) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesResized,
			fmt.Sprintf("auto full drives resized %d container(s): %s; the affected pod(s) must be recreated before the new sizing takes effect (see the per-container AutoFullDrivesResized events)", len(grown), strings.Join(grown, "; ")))
	}
	want := r.desiredRoleCounts(targets)
	room := map[string]int{}
	for _, mode := range []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute} {
		room[mode] = want[mode] - len(live[mode])
	}
	sort.SliceStable(missing, func(i, j int) bool {
		if missing[i].paired != missing[j].paired {
			return missing[i].paired
		}
		return missing[i].node < missing[j].node
	})
	baseTemplate := allocator.GetWekaClusterTemplate(cluster.Spec.Dynamic)
	for _, t := range missing {
		if room[t.mode] <= 0 {
			continue
		}
		room[t.mode]--
		template := baseTemplate
		var hp allocator.ContainerHugepages
		if t.mode == weka.WekaContainerModeDrive {
			template.Cores.Drive = t.cores
			hp = allocator.DriveHugepagesFromPlan(cluster, t.cores, t.numDrives)
		} else {
			template.Cores.Compute = t.cores
			template.Containers.Compute = computeCount
			hp = allocator.ComputeHugepagesFromPlan(cluster, t.computeHugepagesMiB, t.cores)
		}
		name := allocator.NewContainerName(t.mode)
		auto := autoHugepagesFor(cluster, &t, t.cores, t.numDrives)
		var below []string
		hp.Hugepages, hp.HugepagesOffset, below = floorHugepages(fmt.Sprintf("new %s on %s", t.mode, t.node), hp, auto, 0, 0, in.hugepagesPin[t.mode], in.hugepagesOffsetPin[t.mode])
		belowAuto = append(belowAuto, below...)
		container, buildErr := factory.NewWekaContainerForWekaCluster(cluster, template, hp, t.mode, name)
		if buildErr != nil {
			skipped = append(skipped, fmt.Sprintf("role %s container %s: %s", t.mode, name, buildErr))
			continue
		}
		container.Spec.NodeAffinity = weka.NodeName(t.node)
		if t.mode == weka.WekaContainerModeDrive {
			container.Spec.NumDrives = t.numDrives
		}
		built = append(built, container)
	}
	if len(belowAuto) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesHugepagesPinBelowAuto,
			fmt.Sprintf("%d hugepages pin(s) are below the auto value and not applied: %s", len(belowAuto), util.JoinCapped(belowAuto, "; ", 10)))
	}
	return built, computeCount, skipped, errors.Join(growErrs...)
}

// autoHugepagesFor is the hugepages and offset the plan gives t at the given sizing with every pin ignored.
func autoHugepagesFor(cluster *weka.WekaCluster, t *dsContainerTarget, cores, drives int) allocator.ContainerHugepages {
	unpinned := *cluster
	dyn := *cmp.Or(cluster.Spec.Dynamic, &weka.WekaClusterTemplate{})
	dyn.DriveHugepages, dyn.DriveHugepagesOffset, dyn.ComputeHugepages, dyn.ComputeHugepagesOffset = 0, 0, 0, 0
	unpinned.Spec.Dynamic = &dyn
	if t.mode == weka.WekaContainerModeDrive {
		return allocator.DriveHugepagesFromPlan(&unpinned, cores, drives)
	}
	return allocator.ComputeHugepagesFromPlan(&unpinned, t.computeHugepagesMiB, cores)
}

// floorHugepages resolves hugepages and offset to the pin when it is at or above auto, else to auto. pinned is
// hp as planned with the pins; current is the container's value (0 on create), used only in the fragment that
// names a pin below auto.
func floorHugepages(name string, pinned, auto allocator.ContainerHugepages, curHp, curOff int, pinHp, pinOff bool) (hugepages, offset int, below []string) {
	floor := func(field string, isPinned bool, pin, auto, cur int) int {
		switch {
		case !isPinned:
			return auto
		case pin >= auto:
			return pin
		}
		below = append(below, fmt.Sprintf("%s/%s pin=%d auto=%d current=%d", name, field, pin, auto, cur))
		return auto
	}
	hugepages = floor("hugepages", pinHp, pinned.Hugepages, auto.Hugepages, curHp)
	offset = floor("hugepagesOffset", pinOff, pinned.HugepagesOffset, auto.HugepagesOffset, curOff)
	return hugepages, offset, below
}

// raiseDaemonsetContainer updates c in place to t. Cores are never lowered, hugepages never below auto, numDrives
// only when t.drivesFollowFree. Returns "" when c is already there, and a fragment for each pin held back because it is below auto.
func (r *wekaClusterReconcilerLoop) raiseDaemonsetContainer(ctx context.Context, c *weka.WekaContainer, t *dsContainerTarget, pinHp, pinOff bool) (summary string, below []string, err error) {
	cluster := r.cluster
	drive := t.mode == weka.WekaContainerModeDrive
	old := c.Spec
	drivesLowered := t.drivesFollowFree && t.numDrives < c.Spec.NumDrives
	mutate := func(latest *weka.WekaContainer) bool {
		cores := max(latest.Spec.NumCores, t.cores)
		drives := max(latest.Spec.NumDrives, t.numDrives)
		if t.drivesFollowFree {
			drives = t.numDrives
		}
		var hp allocator.ContainerHugepages
		if drive {
			hp = allocator.DriveHugepagesFromPlan(cluster, cores, drives)
		} else {
			hp = allocator.ComputeHugepagesFromPlan(cluster, t.computeHugepagesMiB, cores)
		}
		var hugepages, offset int
		hugepages, offset, below = floorHugepages(c.Name, hp, autoHugepagesFor(cluster, t, cores, drives), latest.Spec.Hugepages, latest.Spec.HugepagesOffset, pinHp, pinOff)
		changed := cores != latest.Spec.NumCores || drives != latest.Spec.NumDrives ||
			hugepages != latest.Spec.Hugepages || offset != latest.Spec.HugepagesOffset
		if !changed {
			return false
		}
		latest.Spec.NumCores = cores
		if drive {
			latest.Spec.NumDrives = drives
		}
		latest.Spec.Hugepages = hugepages
		latest.Spec.HugepagesOffset = offset
		return true
	}
	changed := mutate(c.DeepCopy())
	if len(below) > 0 {
		spec := plannerEventSpecs[reasonAutoFullDrivesHugepagesPinBelowAuto]
		if r.Throttler.ShouldRun("container/"+c.Name+"|"+reasonAutoFullDrivesHugepagesPinBelowAuto, &throttling.ThrottlingSettings{Interval: spec.interval, DisableRandomPreSetInterval: true}) {
			util.RecordEvent(r.Recorder, c, spec.eventType, reasonAutoFullDrivesHugepagesPinBelowAuto, spec.action, util.JoinCapped(below, "; ", 10))
		}
	}
	if !changed {
		return "", below, nil
	}
	alreadyAtTarget, err := r.updateContainerWithRetry(ctx, c, mutate)
	if err != nil {
		return "", below, fmt.Errorf("failed to raise container %s: %w", c.Name, err)
	}
	if alreadyAtTarget {
		return "", below, nil
	}
	var changes []string
	for _, f := range []struct {
		name     string
		from, to int
	}{
		{"numDrives", old.NumDrives, c.Spec.NumDrives},
		{"cores", old.NumCores, c.Spec.NumCores},
		{"hugepages", old.Hugepages, c.Spec.Hugepages},
		{"hugepagesOffset", old.HugepagesOffset, c.Spec.HugepagesOffset},
	} {
		if f.from != f.to {
			changes = append(changes, fmt.Sprintf("%s %d→%d", f.name, f.from, f.to))
		}
	}
	diff := strings.Join(changes, ", ")
	msg := fmt.Sprintf("resized %s container: %s; the pod must be recreated to apply it", t.mode, diff)
	switch {
	case drivesLowered:
		msg += fmt.Sprintf(" (the node can give this container only %d drive(s))", c.Spec.NumDrives)
	case c.Spec.NumDrives > old.NumDrives:
		msg += " (the new drives are reserved now but added to weka only after the pod is recreated)"
	}
	util.RecordEvent(r.Recorder, c, v1.EventTypeWarning, reasonAutoFullDrivesResized, consts.ActionApplyCapacityGrowth, msg)
	return fmt.Sprintf("%s on %s (%s)", c.Name, t.node, diff), below, nil
}

func (r *wekaClusterReconcilerLoop) emitDaemonsetEvents(p *daemonsetPlan, ratio float64) {
	if len(p.unsigned) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesUnsignedDriveNodes,
			fmt.Sprintf("%d drive-role node(s) have no signed full drives and get no containers: %s", len(p.unsigned), util.JoinCapped(p.unsigned, "; ", 10)))
	}
	if len(p.ineligible) > 0 {
		var frags, reasons []string
		for _, node := range slices.Sorted(maps.Keys(p.ineligible)) {
			frags = append(frags, fmt.Sprintf("%s (%s)", node, p.ineligible[node]))
			reasons = append(reasons, p.ineligible[node])
		}
		slices.Sort(reasons)
		r.emitPlannerEventWithCause(reasonAutoFullDrivesNodeIneligible, strings.Join(slices.Compact(reasons), ","),
			fmt.Sprintf("%d node(s) get no new containers: %s", len(frags), util.JoinCapped(frags, "; ", 10)))
	}
	if len(p.noFree) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesNoFreeDrives,
			fmt.Sprintf("%d drive-role node(s) have no free full drives and get no containers: %s", len(p.noFree), util.JoinCapped(p.noFree, "; ", 10)))
	}
	if len(p.unused) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesUnusedDrivesOnNode, util.JoinCapped(p.unused, "; ", 10))
	}
	if len(p.capped) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesComputeCoresCapped,
			fmt.Sprintf("compute cores capped at the per-container maximum for %s; desired compute:drive core ratio %.2f, achieved %.2f",
				util.JoinCapped(p.capped, "; ", 10), max(ratio, 1), float64(p.computeCores)/float64(p.driveCores)))
	}
}

// announceDaemonsetCreated reports the containers whose API Create succeeded.
func (r *wekaClusterReconcilerLoop) announceDaemonsetCreated(created []*weka.WekaContainer) {
	nodes := map[string][]string{}
	for _, c := range created {
		nodes[c.Spec.Mode] = append(nodes[c.Spec.Mode], string(c.Spec.NodeAffinity))
	}
	var parts []string
	for _, mode := range []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute} {
		if n := nodes[mode]; len(n) > 0 {
			slices.Sort(n)
			parts = append(parts, fmt.Sprintf("%d %s container(s) on [%s]", len(n), mode, util.JoinCapped(n, ",", 10)))
		}
	}
	if len(parts) > 0 {
		r.emitPlannerEvent(reasonAutoFullDrivesContainersCreated, "created "+strings.Join(parts, " and "))
	}
}
