package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	"github.com/weka/weka-operator/pkg/util"
)

// groupDriveCapacities renders capacitiesGiB (expected sorted largest-first) as a compact
// run-length-grouped string, e.g. "5x14.0TiB+1x7.0TiB"; "-" for an empty/nil slice.
func groupDriveCapacities(capacitiesGiB []int) string {
	if len(capacitiesGiB) == 0 {
		return "-"
	}
	var terms []string
	count := 1
	for i := 1; i <= len(capacitiesGiB); i++ {
		if i < len(capacitiesGiB) && capacitiesGiB[i] == capacitiesGiB[i-1] {
			count++
			continue
		}
		terms = append(terms, fmt.Sprintf("%dx%s", count, util.HumanReadableGiB(capacitiesGiB[i-1])))
		count = 1
	}
	return strings.Join(terms, "+")
}

func jsonString(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// ---------------------------------------------------------------------------
// explore-nodes rendering
// ---------------------------------------------------------------------------

func renderNodesJSON(nodes []inventory.NodeDetail) (string, error) { return jsonString(nodes) }

func renderNodesTable(nodes []inventory.NodeDetail, detail string) string {
	if detail != "" {
		for i := range nodes {
			n := &nodes[i]
			if n.Node == detail {
				return renderNodeDetail(n)
			}
		}
		return fmt.Sprintf("node %q not found among the selected nodes\n", detail)
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	// MODE ("shared"/"full"/"-") disambiguates TLC: shared-drives (FreeTlcGiB/PhysTlcGiB) and
	// full-drives (FreeFullTlcGiB/PhysFullTlcGiB) pairs are mutually exclusive (see
	// allocator.ParseAllocatorNodeInfo), so summing is safe. DRIVES(free/phys) is full-drives-only.
	fmt.Fprintln(tw, "NODE\tFD\tMODE\tDRIVES(free/phys)\tFREE SIZES\tTLC(free/phys)\tQLC(free/phys)\tCPU(free/alloc)\tHP2Mi(free/alloc)\tMEM MiB(free/alloc)\tWC\tBLOCKED\tDEL\tINELIGIBLE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	var freeTLC, physTLC, freeQLC, physQLC, freeFullDrives, physFullDrives, blocked int
	for i := range nodes {
		n := &nodes[i]
		del := ""
		if n.HasDeletingDriveContainer {
			del = "yes"
		}
		drivesStr := "-"
		// FREE SIZES groups FreeFullDriveCapacitiesGiB (e.g. "5x14.0TiB+1x7.0TiB") so nodes with the
		// same free-drive count/total but a different size mix are distinguishable.
		sizesStr := "-"
		if n.Mode == "full" {
			drivesStr = fmt.Sprintf("%d/%d", n.FreeFullDriveCount, n.PhysFullDriveCount)
			sizesStr = groupDriveCapacities(n.FreeFullDriveCapacitiesGiB)
		}
		tlcFree := n.FreeTlcGiB + n.FreeFullTlcGiB
		tlcPhys := n.PhysTlcGiB + n.PhysFullTlcGiB
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s / %s\t%s / %s\t%d / %d\t%d / %d\t%d / %d\t%d\t%d\t%s\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			n.Node, dashIfEmpty(n.FDValue), dashIfEmpty(n.Mode), drivesStr, sizesStr,
			util.HumanReadableGiB(tlcFree), util.HumanReadableGiB(tlcPhys),
			util.HumanReadableGiB(n.FreeQlcGiB), util.HumanReadableGiB(n.PhysQlcGiB),
			n.FreeCores, n.AllocatableCores,
			n.FreeHugepagesMiB, n.AllocatableHugepagesMiB,
			n.FreeMemoryMiB, n.AllocatableMemoryMiB,
			len(n.Consumers), n.BlockedFullDriveCount, del, dashIfEmpty(n.IneligibleReason))
		freeTLC += tlcFree
		physTLC += tlcPhys
		freeQLC += n.FreeQlcGiB
		physQLC += n.PhysQlcGiB
		freeFullDrives += n.FreeFullDriveCount
		physFullDrives += n.PhysFullDriveCount
		blocked += n.BlockedFullDriveCount
	}
	fmt.Fprintf(tw, "TOTAL\t\t\t%d/%d\t\t%s / %s\t%s / %s\t\t\t\t%d\t%d\t\t\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		freeFullDrives, physFullDrives,
		util.HumanReadableGiB(freeTLC), util.HumanReadableGiB(physTLC),
		util.HumanReadableGiB(freeQLC), util.HumanReadableGiB(physQLC), len(nodes), blocked)
	tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	return buf.String()
}

// driveListOrNone renders capacitiesGiB as an exact comma-separated list (e.g. "14307, 7153, 7153"),
// or "(none)" when empty.
func driveListOrNone(capacitiesGiB []int) string {
	if len(capacitiesGiB) == 0 {
		return "(none)"
	}
	parts := make([]string, len(capacitiesGiB))
	for i, g := range capacitiesGiB {
		parts[i] = fmt.Sprintf("%d", g)
	}
	return strings.Join(parts, ", ")
}

func renderNodeDetail(n *inventory.NodeDetail) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Node %s (FD %s, mode %s)\n", n.Node, dashIfEmpty(n.FDValue), dashIfEmpty(n.Mode)) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	if n.IneligibleReason != "" {
		fmt.Fprintf(&buf, "  INELIGIBLE: %s\n", n.IneligibleReason) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}
	// TLC free/phys sums shared-drives and full-drives capacity (mutually exclusive per node, see MODE
	// in renderNodesTable); DRIVES below is full-drives-only.
	fmt.Fprintf(&buf, "  TLC free/phys: %s / %s   QLC free/phys: %s / %s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		util.HumanReadableGiB(n.FreeTlcGiB+n.FreeFullTlcGiB), util.HumanReadableGiB(n.PhysTlcGiB+n.PhysFullTlcGiB),
		util.HumanReadableGiB(n.FreeQlcGiB), util.HumanReadableGiB(n.PhysQlcGiB))
	if n.Mode == "full" {
		fmt.Fprintf(&buf, "  DRIVES free/phys: %d / %d", n.FreeFullDriveCount, n.PhysFullDriveCount) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		if n.BlockedFullDriveCount > 0 {
			fmt.Fprintf(&buf, "   BLOCKED: %d", n.BlockedFullDriveCount) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
		fmt.Fprintln(&buf) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		// Exact per-drive capacities (GiB); the table only shows the grouped form (see groupDriveCapacities).
		fmt.Fprintf(&buf, "  Free drives (GiB):    %s\n", driveListOrNone(n.FreeFullDriveCapacitiesGiB))    //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  Claimed drives (GiB): %s\n", driveListOrNone(n.ClaimedFullDriveCapacitiesGiB)) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}
	fmt.Fprintf(&buf, "  CPU free/alloc: %d / %d   HP2Mi free/alloc: %d / %d MiB   MEM free/alloc: %d / %d MiB\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		n.FreeCores, n.AllocatableCores, n.FreeHugepagesMiB, n.AllocatableHugepagesMiB, n.FreeMemoryMiB, n.AllocatableMemoryMiB)
	if len(n.Consumers) == 0 {
		fmt.Fprintln(&buf, "  (no weka containers on this node)") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		return buf.String()
	}
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  CONTAINER\tCLUSTER\tROLE\tTLC\tQLC\tCORES\tHP(MiB)\tNILRATIO\tDEL") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	for _, c := range n.Consumers {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			c.Name, dashIfEmpty(c.Cluster), c.Role,
			util.HumanReadableGiB(c.TlcGiB), util.HumanReadableGiB(c.QlcGiB),
			c.Cores, c.HugepagesMiB, yesIf(c.NilRatio), yesIf(c.MarkedForDeletion))
	}
	tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	return buf.String()
}

// ---------------------------------------------------------------------------
// plan rendering
// ---------------------------------------------------------------------------

func renderPlanJSON(d *planData) (string, error) {
	type nodeRej struct {
		Node      string `json:"node"`
		Binding   string `json:"binding"`
		FreeGiB   int    `json:"freeGiB"`
		NeededGiB int    `json:"neededGiB"`
		// Needed/Available/Unit carry a NON-capacity rejection (physical CPU, hugepages, memory) from the
		// auto-full-drives node-fit gate. Kept separate from FreeGiB/NeededGiB rather than overloading them:
		// those two are GiB by contract and a consumer humanizing them would render a CPU count as "16 GiB".
		// Omitted when Unit is empty, so a capacity rejection's payload is unchanged.
		Needed    int    `json:"needed,omitempty"`
		Available int    `json:"available,omitempty"`
		Unit      string `json:"unit,omitempty"`
	}
	out := map[string]any{
		"cluster": d.Cluster,
		"desired": map[string]any{
			"clusterCapacity": d.ClusterCapacity,
			"driveTypesRatio": d.Ratio,
			"protection":      map[string]int{"stripeWidth": d.SW, "redundancyLevel": d.RL, "hotSpare": d.HS},
			"tlcRawGiB":       d.DesiredTlcRaw,
			"qlcRawGiB":       d.DesiredQlcRaw,
			"minChunkGiB":     d.MinChunkGiB,
		},
		"current":  map[string]int{"tlcGiB": d.CurrentTlc, "qlcGiB": d.CurrentQlc},
		"feasible": d.Plan.Infeasible == "",
	}
	if r := d.Plan.Infeasibility; r != nil {
		rej := make([]nodeRej, 0, len(r.RejectedNodes))
		for _, n := range r.RejectedNodes {
			rej = append(rej, nodeRej{
				Node: n.Node, Binding: n.Binding, FreeGiB: n.FreeGiB, NeededGiB: n.NeededGiB,
				Needed: n.Needed, Available: n.Available, Unit: n.Unit,
			})
		}
		out["infeasibility"] = map[string]any{
			"reason":        r.Reason,
			"pool":          r.Pool,
			"binding":       r.Binding,
			"shortfallGiB":  r.ShortfallGiB,
			"rejectedNodes": rej,
			"fixes":         r.Fixes,
		}
	}
	out["createDrive"] = d.Plan.Create
	out["growDrive"] = d.Plan.Grow
	out["createCompute"] = d.ComputeCreate
	out["growCompute"] = d.ComputeGrow
	out["warnings"] = capacityplanner.WarningMessages(d.Plan.Warnings)
	out["overProvisions"] = d.Plan.OverProvisions
	out["shrinkEvents"] = d.Plan.ShrinkEvents
	out["summary"] = d.Summary
	return jsonString(out)
}

func renderPlanText(d *planData) string {
	var buf bytes.Buffer
	p := d.Plan

	fmt.Fprintf(&buf, "CLUSTER %s\n", d.Cluster) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice

	// TARGET: the requested end state (what the cluster spec asks for).
	fmt.Fprintln(&buf, "\nTARGET") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	twTgt := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	fmt.Fprintf(twTgt, "  usable capacity\t%s\n", d.ClusterCapacity)                           //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(twTgt, "  drive ratio\t%s\n", d.Ratio)                                         //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(twTgt, "  protection\t%d+%d+%d  (stripe+redundancy+hotSpare → minFdNum %d)\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		d.SW, d.RL, d.HS, d.SW+d.RL+d.HS)
	fmt.Fprintf(twTgt, "  min chunk\t%s\n", util.HumanReadableGiB(d.MinChunkGiB)) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	twTgt.Flush()                                                                 //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice

	// RAW CAPACITY: current vs target raw capacity and the per-column delta (what must change).
	fmt.Fprint(&buf, "\n") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	twRaw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	fmt.Fprintln(twRaw, "RAW CAPACITY\tTLC\tQLC\ttotal") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(twRaw, "  current\t%s\t%s\t%s\n",        //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		util.HumanReadableGiB(d.CurrentTlc), util.HumanReadableGiB(d.CurrentQlc),
		util.HumanReadableGiB(d.CurrentTlc+d.CurrentQlc))
	fmt.Fprintf(twRaw, "  target\t%s\t%s\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		util.HumanReadableGiB(d.DesiredTlcRaw), util.HumanReadableGiB(d.DesiredQlcRaw),
		util.HumanReadableGiB(d.DesiredTlcRaw+d.DesiredQlcRaw))
	fmt.Fprintf(twRaw, "  delta\t%s\t%s\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		signedGiB(d.DesiredTlcRaw-d.CurrentTlc), signedGiB(d.DesiredQlcRaw-d.CurrentQlc),
		signedGiB((d.DesiredTlcRaw+d.DesiredQlcRaw)-(d.CurrentTlc+d.CurrentQlc)))
	twRaw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice

	if p.Infeasible == "" {
		fmt.Fprintln(&buf, "\nFEASIBILITY  OK") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	} else {
		fmt.Fprintln(&buf, "\nFEASIBILITY  INFEASIBLE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}

	if r := p.Infeasibility; r != nil {
		fmt.Fprintln(&buf, "\nINFEASIBLE")                              //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  reason: %s\n", r.Reason)                   //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  pool: %s   binding: %s   shortfall: %s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			dashIfEmpty(r.Pool), dashIfEmpty(r.Binding), util.HumanReadableGiB(r.ShortfallGiB))
		renderRejectedNodes(&buf, r.RejectedNodes)
		if len(r.Fixes) > 0 {
			fmt.Fprintln(&buf, "  FIXES:") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for i, f := range r.Fixes {
				fmt.Fprintf(&buf, "    %d. %s\n", i+1, f) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			}
		}
	}

	// On an infeasible plan the controller discards the whole plan, so any create/grow rows below are
	// only the partial placement reached before the binding pool; relabel headers so they read as non-actionable.
	createLabel, growLabel := "create", "grow"
	if p.Infeasible != "" {
		createLabel = "create (PARTIAL — NOT applied; plan is infeasible)"
		growLabel = "grow (PARTIAL — NOT applied; plan is infeasible)"
	}

	// DRIVE: create rows keyed by node, grow rows by container name. Grow cells show the from→to
	// transition per column, collapsing to a single value when unchanged.
	if len(p.Create) > 0 || len(d.DriveGrow) > 0 {
		fmt.Fprintln(&buf, "\nDRIVE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		if len(p.Create) > 0 {
			fmt.Fprintln(&buf, "  "+createLabel) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "    NODE\tFD\tTYPE\tTLC\tQLC\tCORES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, c := range p.Create {
				fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\t%s\t%d\n", c.Node, dashIfEmpty(c.FDValue), c.Type, //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
					util.HumanReadableGiB(c.TlcGiB), util.HumanReadableGiB(c.QlcGiB), c.NumCores)
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
		if len(d.DriveGrow) > 0 {
			fmt.Fprintln(&buf, "  "+growLabel) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "    CONTAINER\tNODE\tTLC\tQLC\tCORES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, g := range d.DriveGrow {
				fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\t%s\n", g.Name, dashIfEmpty(g.Node), //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
					transitionGiB(g.FromTlcGiB, g.ToTlcGiB),
					transitionGiB(g.FromQlcGiB, g.ToQlcGiB),
					transitionInt(g.FromCores, g.ToCores))
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
	}

	// COMPUTE: create rows keyed by node, grow rows by container name, showing the core transition.
	if len(d.ComputeCreate) > 0 || len(d.ComputeGrow) > 0 {
		fmt.Fprintln(&buf, "\nCOMPUTE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		if len(d.ComputeCreate) > 0 {
			fmt.Fprintln(&buf, "  "+createLabel) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "    NODE\tCORES\tHUGEPAGES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, r := range d.ComputeCreate {
				fmt.Fprintf(tw, "    %s\t%d\t%d\n", r.Node, r.ToCores, r.HugepagesMiB) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
		if len(d.ComputeGrow) > 0 {
			fmt.Fprintln(&buf, "  "+growLabel) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "    CONTAINER\tNODE\tCORES\tHUGEPAGES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, r := range d.ComputeGrow {
				fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n", dashIfEmpty(r.Name), dashIfEmpty(r.Node), //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
					transitionInt(r.FromCores, r.ToCores), transitionInt(r.FromHugepagesMiB, r.HugepagesMiB))
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
	}

	renderList(&buf, "WARNINGS", capacityplanner.WarningMessages(p.Warnings))
	renderList(&buf, "OVER-PROVISION", p.OverProvisions)
	renderList(&buf, "SHRINK", p.ShrinkEvents)

	fmt.Fprintln(&buf, "\nSUMMARY")        //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(&buf, "  %s\n", d.Summary) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	return buf.String()
}

func renderList(buf *bytes.Buffer, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(buf, "\n%s\n", title) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	for _, m := range items {
		fmt.Fprintf(buf, "  - %s\n", m) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesIf(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

// signedGiB formats a whole-GiB delta with an explicit leading sign. util.HumanReadableGiB takes a
// magnitude and does not add a sign, so negatives are formatted from their absolute value.
func signedGiB(gib int) string {
	if gib < 0 {
		return "-" + util.HumanReadableGiB(-gib)
	}
	return "+" + util.HumanReadableGiB(gib)
}

// transitionGiB renders an in-place grow of a GiB value as "from→to", collapsing to a single value when
// the column does not change (e.g. TLC on a QLC-only container stays 0B rather than showing "0B→0B").
func transitionGiB(from, to int) string {
	if from == to {
		return util.HumanReadableGiB(to)
	}
	return util.HumanReadableGiB(from) + "→" + util.HumanReadableGiB(to)
}

// transitionInt renders an in-place grow of an integer (e.g. cores) as "from→to", collapsing to a single
// value when unchanged.
func transitionInt(from, to int) string {
	if from == to {
		return fmt.Sprintf("%d", to)
	}
	return fmt.Sprintf("%d→%d", from, to)
}

// renderRejectedNodes prints the per-node rejection table shared by both plan renderers: every node the
// planner could not place on, with the dimension that bound it and what it had versus what it needed.
//
// The FREE/NEEDED pair is unit-aware. A capacity rejection leaves NodeRejection.Unit empty and carries
// GiB in FreeGiB/NeededGiB; the auto-full-drives node-fit gate instead sets Unit ("physical CPU", "MiB
// hugepages", …) with plain counts in Available/Needed. Running the latter through the GiB humanizer
// would print 16 physical CPUs as "16 GiB", so the unit decides the formatting.
func renderRejectedNodes(buf *bytes.Buffer, rejected []capacityplanner.NodeRejection) {
	if len(rejected) == 0 {
		return
	}
	tw := tabwriter.NewWriter(buf, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  NODE\tBINDING\tFREE\tNEEDED") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	for _, n := range rejected {
		free, needed := util.HumanReadableGiB(n.FreeGiB), util.HumanReadableGiB(n.NeededGiB)
		if n.Unit != "" {
			free = fmt.Sprintf("%d %s", n.Available, n.Unit)
			needed = fmt.Sprintf("%d %s", n.Needed, n.Unit)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", n.Node, dashIfEmpty(n.Binding), free, needed) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}
	tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
}

// ---------------------------------------------------------------------------
// auto-full-drives (daemonset) plan rendering
// ---------------------------------------------------------------------------

func renderAutoFullDrivesPlanJSON(d *autoFullDrivesPlanData) (string, error) {
	out := map[string]any{
		"cluster":       d.Cluster,
		"mode":          "autoFullDrives",
		"feasible":      d.Plan.Infeasible == "",
		"nodes":         d.Nodes,
		"createDrive":   d.Plan.Create,
		"growDrive":     d.Plan.Grow,
		"createCompute": d.ComputeCreate,
		"growCompute":   d.ComputeGrow,
		"warnings":      capacityplanner.WarningMessages(d.Plan.Warnings),
		"summary":       d.Summary,
	}
	if r := d.Plan.Infeasibility; r != nil {
		out["infeasibility"] = r
	}
	out["driveSizing"] = d.Plan.DriveSizing
	return jsonString(out)
}

func renderAutoFullDrivesPlanText(d *autoFullDrivesPlanData) string {
	var buf bytes.Buffer
	p := d.Plan

	fmt.Fprintf(&buf, "CLUSTER %s (daemonset / auto full drives)\n", d.Cluster) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice

	if p.Infeasible == "" {
		fmt.Fprintln(&buf, "\nFEASIBILITY  OK") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	} else {
		fmt.Fprintln(&buf, "\nFEASIBILITY  INFEASIBLE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}

	if r := p.Infeasibility; r != nil {
		fmt.Fprintln(&buf, "\nINFEASIBLE")                              //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  reason: %s\n", r.Reason)                   //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  pool: %s   binding: %s   shortfall: %s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			dashIfEmpty(r.Pool), dashIfEmpty(r.Binding), util.HumanReadableGiB(r.ShortfallGiB))
		// Every node that could not fit a container sized for all its drives. In this mode ONE such node
		// makes the whole plan infeasible, so this table is the primary diagnostic.
		renderRejectedNodes(&buf, r.RejectedNodes)
		if len(r.Fixes) > 0 {
			fmt.Fprintln(&buf, "  FIXES:") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for i, f := range r.Fixes {
				fmt.Fprintf(&buf, "    %d. %s\n", i+1, f) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			}
		}
	}

	// DRIVE SIZING is a flat statement of what the planner sized, printed whenever it ran (even on an
	// infeasible plan). It is deliberately NOT an explanation of anything held back: drive cores are
	// derived once from the drive count (or the driveCores pin) and are never traded away to fit
	// compute, so there is no cap, no attempt count, and no per-node "limited" set to report. A fleet
	// that cannot host the compute those cores require is infeasible, and the INFEASIBLE section above
	// carries that. ds.Reason is the planner's own one-line rationale.
	if ds := p.DriveSizing; ds != nil {
		fmt.Fprintln(&buf, "\nDRIVE SIZING")                      //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  drives: %d/%d taken   TLC: %s/%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			ds.DrivesTaken, ds.DrivesAvailable, util.HumanReadableGiB(ds.TlcGiBTaken), util.HumanReadableGiB(ds.TlcGiBAvailable))
		fmt.Fprintf(&buf, "  drive cores: %d   compute cores required: %d\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			ds.TotalTlcDriveCores, ds.RequiredComputeCores)
		fmt.Fprintf(&buf, "  compute: %d container(s), %d cores/container, %d MiB hugepages\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			ds.ComputeContainers, ds.ComputeCoresPerContainer, ds.ComputeHugepagesMiB)
		fmt.Fprintf(&buf, "  rationale: %s\n", ds.Reason) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}

	// NODES: one row per node with a signed full drive — drives used/avail, TLC, cores, and STATE
	// (create/grow/existing/not-planned). NOTE explains a row holding fewer drives than it offers and points
	// at the WARNINGS list below for the fleet-wide detail.
	if len(d.Nodes) > 0 {
		fmt.Fprintln(&buf, "\nNODES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  NODE\tFD\tDRIVES(used/avail)\tTLC\tCORES\tSTATE\tNOTE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		for _, n := range d.Nodes {
			fmt.Fprintf(tw, "  %s\t%s\t%d/%d\t%s\t%d\t%s\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
				n.Node, dashIfEmpty(n.FD), n.DrivesUsed, n.DrivesAvail,
				util.HumanReadableGiB(n.TlcGiB), n.Cores, n.State, dashIfEmpty(n.Note))
		}
		tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}

	// DRIVE GROW: the daemonset mode's expand-only growth, additionally showing the drive-count transition
	// (unlike clusterCapacity's DRIVE grow — see planner.go). Drives and cores move independently, so a row
	// may show a drive change with cores unchanged (free, applies live) or the reverse.
	if len(d.DriveGrow) > 0 {
		fmt.Fprintln(&buf, "\nDRIVE GROW (A4, expand-only)") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  CONTAINER\tNODE\tDRIVES\tTLC\tCORES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		for _, g := range d.DriveGrow {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", g.Name, dashIfEmpty(g.Node), //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
				transitionInt(g.FromNumDrives, g.ToNumDrives),
				transitionGiB(g.FromTlcGiB, g.ToTlcGiB),
				transitionInt(g.FromCores, g.ToCores))
		}
		tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	}

	// COMPUTE: identical shape to clusterCapacity's tables — same ComputeContainerSpec-derived model
	// (see planComputeAutoFullDrives), fed by a different drive plan.
	if len(d.ComputeCreate) > 0 || len(d.ComputeGrow) > 0 {
		fmt.Fprintln(&buf, "\nCOMPUTE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		if len(d.ComputeCreate) > 0 {
			fmt.Fprintln(&buf, "  create") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "    NODE\tCORES\tHUGEPAGES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, r := range d.ComputeCreate {
				fmt.Fprintf(tw, "    %s\t%d\t%d\n", r.Node, r.ToCores, r.HugepagesMiB) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
		if len(d.ComputeGrow) > 0 {
			fmt.Fprintln(&buf, "  grow") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "    CONTAINER\tNODE\tCORES\tHUGEPAGES") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, r := range d.ComputeGrow {
				fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n", dashIfEmpty(r.Name), dashIfEmpty(r.Node), //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
					transitionInt(r.FromCores, r.ToCores), transitionInt(r.FromHugepagesMiB, r.HugepagesMiB))
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
	}

	renderList(&buf, "WARNINGS", capacityplanner.WarningMessages(p.Warnings))

	fmt.Fprintln(&buf, "\nSUMMARY")        //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(&buf, "  %s\n", d.Summary) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	return buf.String()
}
