package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	"github.com/weka/weka-operator/pkg/util"
)

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
	fmt.Fprintln(tw, "NODE\tFD\tTLC(free/phys)\tQLC(free/phys)\tCPU(free/alloc)\tHP2Mi(free/alloc)\tMEM MiB(free/alloc)\tWC\tDEL") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	var freeTLC, physTLC, freeQLC, physQLC int
	for i := range nodes {
		n := &nodes[i]
		del := ""
		if n.HasDeletingDriveContainer {
			del = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s/%s\t%s/%s\t%d/%d\t%d/%d\t%d/%d\t%d\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			n.Node, dashIfEmpty(n.FDValue),
			util.HumanReadableGiB(n.FreeTlcGiB), util.HumanReadableGiB(n.PhysTlcGiB),
			util.HumanReadableGiB(n.FreeQlcGiB), util.HumanReadableGiB(n.PhysQlcGiB),
			n.FreeCores, n.AllocatableCores,
			n.FreeHugepagesMiB, n.AllocatableHugepagesMiB,
			n.FreeMemoryMiB, n.AllocatableMemoryMiB,
			len(n.Consumers), del)
		freeTLC += n.FreeTlcGiB
		physTLC += n.PhysTlcGiB
		freeQLC += n.FreeQlcGiB
		physQLC += n.PhysQlcGiB
	}
	fmt.Fprintf(tw, "TOTAL\t\t%s/%s\t%s/%s\t\t\t\t%d\t\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		util.HumanReadableGiB(freeTLC), util.HumanReadableGiB(physTLC),
		util.HumanReadableGiB(freeQLC), util.HumanReadableGiB(physQLC), len(nodes))
	tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	return buf.String()
}

func renderNodeDetail(n *inventory.NodeDetail) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Node %s (FD %s)\n", n.Node, dashIfEmpty(n.FDValue)) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(&buf, "  TLC free/phys: %s/%s   QLC free/phys: %s/%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		util.HumanReadableGiB(n.FreeTlcGiB), util.HumanReadableGiB(n.PhysTlcGiB),
		util.HumanReadableGiB(n.FreeQlcGiB), util.HumanReadableGiB(n.PhysQlcGiB))
	fmt.Fprintf(&buf, "  CPU free/alloc: %d/%d   HP2Mi free/alloc: %d/%d MiB   MEM free/alloc: %d/%d MiB\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
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
			rej = append(rej, nodeRej{n.Node, n.Binding, n.FreeGiB, n.NeededGiB})
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
	out["warnings"] = d.Plan.Warnings
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
	fmt.Fprintf(twTgt, "  usable capacity\t%s\n", d.ClusterCapacity) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(twTgt, "  drive ratio\t%s\n", d.Ratio) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(twTgt, "  protection\t%d+%d+%d  (stripe+redundancy+hotSpare → minFdNum %d)\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		d.SW, d.RL, d.HS, d.SW+d.RL+d.HS)
	fmt.Fprintf(twTgt, "  min chunk\t%s\n", util.HumanReadableGiB(d.MinChunkGiB)) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	twTgt.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice

	// RAW CAPACITY: current vs target raw capacity and the per-column delta (what must change).
	fmt.Fprint(&buf, "\n") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	twRaw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	fmt.Fprintln(twRaw, "RAW CAPACITY\tTLC\tQLC\ttotal") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
	fmt.Fprintf(twRaw, "  current\t%s\t%s\t%s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
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
		fmt.Fprintln(&buf, "\nINFEASIBLE") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  reason: %s\n", r.Reason) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		fmt.Fprintf(&buf, "  pool: %s   binding: %s   shortfall: %s\n", //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			dashIfEmpty(r.Pool), dashIfEmpty(r.Binding), util.HumanReadableGiB(r.ShortfallGiB))
		if len(r.RejectedNodes) > 0 {
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "  NODE\tBINDING\tFREE\tNEEDED") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for _, n := range r.RejectedNodes {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", n.Node, n.Binding, //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
					util.HumanReadableGiB(n.FreeGiB), util.HumanReadableGiB(n.NeededGiB))
			}
			tw.Flush() //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
		}
		if len(r.Fixes) > 0 {
			fmt.Fprintln(&buf, "  FIXES:") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			for i, f := range r.Fixes {
				fmt.Fprintf(&buf, "    %d. %s\n", i+1, f) //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
			}
		}
	}

	// On an infeasible plan the controller discards the whole plan (creates/grows nothing), so any
	// create/grow rows below are only the partial placement the planner reached before hitting the
	// binding pool. Relabel the sub-headers so they are never read as actionable.
	createLabel, growLabel := "create", "grow"
	if p.Infeasible != "" {
		createLabel = "create (PARTIAL — NOT applied; plan is infeasible)"
		growLabel = "grow (PARTIAL — NOT applied; plan is infeasible)"
	}

	// DRIVE: one section, with create / grow sub-groups. Create rows are keyed by node (NODE), grow rows
	// by container name (CONTAINER). Grow cells show the current→target transition (from→to) per column,
	// mirroring compute grow; a column that does not change shows the single value.
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

	// COMPUTE: same create / grow sub-group treatment. Grow shows the core transition (from→to); create
	// rows are keyed by node, grow rows by container name.
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

	renderList(&buf, "WARNINGS", p.Warnings)
	renderList(&buf, "OVER-PROVISION", p.OverProvisions)
	renderList(&buf, "SHRINK", p.ShrinkEvents)

	fmt.Fprintln(&buf, "\nSUMMARY") //nolint:errcheck // writes to an in-memory buffer/tabwriter; cannot fail in practice
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
