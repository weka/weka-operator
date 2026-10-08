package wekacluster

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1/condition"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

func dsCons(maxCores int, ratio float64) *capacityplanner.CapacityConstraints {
	return &capacityplanner.CapacityConstraints{
		MaxCoresPerContainer: maxCores, FullDrivesComputeToDriveCoreRatio: ratio, ComputeHugepagesTlcRatio: 1000,
	}
}

func drives(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = 1000
	}
	return out
}

// shared is a node matching both selectors.
func shared(nDrives int) dsNode {
	return dsNode{driveRole: true, computeRole: true, drivesGiB: drives(nDrives), listed: true, availGiB: drives(nDrives)}
}

func driveOnly(nDrives int) dsNode {
	return dsNode{driveRole: true, drivesGiB: drives(nDrives), listed: true, availGiB: drives(nDrives)}
}

// sharedFree is a shared node with signed drives, of which only avail are usable by this cluster.
func sharedFree(signed int, avail ...int) dsNode {
	return dsNode{driveRole: true, computeRole: true, drivesGiB: drives(signed), listed: true, availGiB: avail}
}

func computeOnly() dsNode { return dsNode{computeRole: true} }

func ctr(drives, cores int) *weka.WekaContainer {
	return &weka.WekaContainer{Spec: weka.WekaContainerSpec{NumDrives: drives, NumCores: cores}}
}

func liveOf(drive, compute map[string]*weka.WekaContainer) map[string]map[string]*weka.WekaContainer {
	return map[string]map[string]*weka.WekaContainer{weka.WekaContainerModeDrive: drive, weka.WekaContainerModeCompute: compute}
}

type dsWant struct {
	mode, node string
	drives     int // drive only
	cores      int
	hugepages  int // compute only; 0 = not checked
}

func TestPlanDaemonset(t *testing.T) {
	cons := dsCons(19, 2)
	hp := func(tlc, count, cores int) int {
		return capacityplanner.ComputeContainerHugepagesMiB(tlc, 0, count, cores, cons)
	}
	d := func(node string, drives, cores int) dsWant {
		return dsWant{mode: "drive", node: node, drives: drives, cores: cores}
	}
	c := func(node string, cores, hugepages int) dsWant {
		return dsWant{mode: "compute", node: node, cores: cores, hugepages: hugepages}
	}

	for _, tc := range []struct {
		name           string
		in             daemonsetInput
		want           []dsWant
		unsigned       []string
		ineligible     map[string]string
		unused, noFree []string
		capped         int
		statusD, statC int
	}{
		{
			name:    "shared homogeneous selector",
			in:      daemonsetInput{nodes: map[string]dsNode{"n1": shared(8), "n2": shared(8)}, cons: cons},
			want:    []dsWant{d("n1", 8, 8), d("n2", 8, 8), c("n1", 16, hp(8000, 1, 16)), c("n2", 16, hp(8000, 1, 16))},
			statusD: 2, statC: 2,
		},
		{
			name:    "disjoint selectors spread evenly",
			in:      daemonsetInput{nodes: map[string]dsNode{"d1": driveOnly(4), "d2": driveOnly(4), "c1": computeOnly(), "c2": computeOnly(), "c3": computeOnly()}, cons: cons},
			want:    []dsWant{d("d1", 4, 4), d("d2", 4, 4), c("c1", 6, hp(8000, 3, 6)), c("c2", 6, hp(8000, 3, 6)), c("c3", 6, hp(8000, 3, 6))},
			statusD: 2, statC: 3,
		},
		{
			name: "partial overlap pairs then spreads the remainder",
			in: daemonsetInput{nodes: map[string]dsNode{
				"n1": shared(4), "n2": shared(4), "n3": driveOnly(4), "x1": computeOnly(), "x2": computeOnly(),
			}, cons: cons},
			// required 24, paired 16, remainder 8 over 2 nodes
			want: []dsWant{
				d("n1", 4, 4), d("n2", 4, 4), d("n3", 4, 4),
				c("n1", 8, hp(4000, 1, 8)), c("n2", 8, hp(4000, 1, 8)), c("x1", 4, hp(4000, 2, 4)), c("x2", 4, hp(4000, 2, 4)),
			},
			statusD: 3, statC: 4,
		},
		{
			name:    "remainder at or below zero gives one core",
			in:      daemonsetInput{nodes: map[string]dsNode{"n1": shared(4), "n2": shared(4), "x1": computeOnly()}, cons: cons},
			want:    []dsWant{d("n1", 4, 4), d("n2", 4, 4), c("n1", 8, hp(4000, 1, 8)), c("n2", 8, hp(4000, 1, 8)), c("x1", 1, hp(0, 1, 1))},
			statusD: 2, statC: 3,
		},
		{
			name: "heterogeneous drives size each node on its own",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(6), "n2": shared(4), "n3": shared(2)}, cons: cons},
			want: []dsWant{
				d("n1", 6, 6), d("n2", 4, 4), d("n3", 2, 2),
				c("n1", 12, hp(6000, 1, 12)), c("n2", 8, hp(4000, 1, 8)), c("n3", 4, hp(2000, 1, 4)),
			},
			statusD: 3, statC: 3,
		},
		{
			name:     "unannotated node gets nothing and is listed",
			in:       daemonsetInput{nodes: map[string]dsNode{"n1": shared(4), "n2": {driveRole: true, computeRole: true}}, cons: cons},
			want:     []dsWant{d("n1", 4, 4), c("n1", 8, hp(4000, 1, 8))},
			unsigned: []string{"n2"}, statusD: 2, statC: 2,
		},
		{
			name: "existing drive container on a node with no usable drives gets no new compute",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": {driveRole: true, computeRole: true}},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(4, 4)}, nil),
				cons:  cons,
			},
			want:    []dsWant{d("n1", 4, 4)},
			statusD: 1, statC: 1,
		},
		{
			name: "ineligible new node gets nothing",
			in: daemonsetInput{nodes: map[string]dsNode{
				"n1": shared(4), "n2": {driveRole: true, computeRole: true, ineligible: "cordoned", drivesGiB: drives(4)},
			}, cons: cons},
			want:       []dsWant{d("n1", 4, 4), c("n1", 8, hp(4000, 1, 8))},
			ineligible: map[string]string{"n2": "cordoned"}, statusD: 1, statC: 1,
		},
		{
			name: "ineligible node with containers still grows",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n2": {driveRole: true, computeRole: true, ineligible: "cordoned", drivesGiB: drives(6), listed: true, availGiB: drives(6)}},
				live:  liveOf(map[string]*weka.WekaContainer{"n2": ctr(4, 4)}, map[string]*weka.WekaContainer{"n2": ctr(0, 8)}),
				cons:  cons,
			},
			want: []dsWant{d("n2", 6, 6), c("n2", 12, hp(6000, 1, 12))}, statusD: 1, statC: 1,
		},
		{
			name: "existing containers on an off-selector node count",
			in: daemonsetInput{
				live: liveOf(map[string]*weka.WekaContainer{"z": ctr(3, 3)}, map[string]*weka.WekaContainer{"z": ctr(0, 2)}),
				cons: cons,
			},
			want: []dsWant{d("z", 3, 3), c("z", 6, 0)}, statusD: 1, statC: 1,
		},
		{
			name: "deleting drive container: node skipped for both roles",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(4), "n2": shared(4)}, deleting: map[string]map[string]bool{weka.WekaContainerModeDrive: {"n1": true}}, cons: cons},
			want: []dsWant{d("n2", 4, 4), c("n2", 8, hp(4000, 1, 8))}, statusD: 2, statC: 2,
		},
		{
			name: "deleting compute container: only compute skipped",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(4), "n2": shared(4)}, deleting: map[string]map[string]bool{weka.WekaContainerModeCompute: {"n1": true}}, cons: cons},
			want: []dsWant{d("n1", 4, 4), d("n2", 4, 4), c("n2", 8, hp(4000, 1, 8))}, statusD: 2, statC: 2,
		},
		{
			name:   "numDrives pin below signed count",
			in:     daemonsetInput{nodes: map[string]dsNode{"n1": shared(8)}, numDrivesPin: 5, cons: cons},
			want:   []dsWant{d("n1", 5, 5), c("n1", 10, hp(5000, 1, 10))},
			unused: []string{"3 of 8 signed drives unused on n1 (numDrives pin=5)"}, statusD: 1, statC: 1,
		},
		{
			name: "numDrives pin above signed count is written verbatim",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(8)}, numDrivesPin: 10, cons: cons},
			want: []dsWant{d("n1", 10, 10), c("n1", 19, 0)}, capped: 1, statusD: 1, statC: 1,
		},
		{
			name: "driveCores pin",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(8)}, driveCoresPin: 3, cons: cons},
			want: []dsWant{d("n1", 8, 3), c("n1", 6, 0)}, statusD: 1, statC: 1,
		},
		{
			name: "computeCores pin on paired and compute-only",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(8), "x1": computeOnly()}, computeCoresPin: 4, cons: cons},
			want: []dsWant{d("n1", 8, 8), c("n1", 4, 0), c("x1", 4, 0)}, statusD: 1, statC: 2,
		},
		{
			name: "cap with default ratio",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(12)}, cons: dsCons(19, 2)},
			want: []dsWant{d("n1", 12, 12), c("n1", 19, 0)}, capped: 1, statusD: 1, statC: 1,
		},
		{
			name: "cap with explicit ratio",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(12)}, cons: dsCons(19, 4)},
			want: []dsWant{d("n1", 12, 12), c("n1", 19, 0)}, capped: 1, statusD: 1, statC: 1,
		},
		{
			name: "no cap when MaxCoresPerContainer is 0",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(24)}, cons: dsCons(0, 2)},
			want: []dsWant{d("n1", 24, 24), c("n1", 48, 0)}, statusD: 1, statC: 1,
		},
		{
			name: "ratio below 1 keeps the 1:1 floor",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": shared(8)}, cons: dsCons(19, 0.5)},
			want: []dsWant{d("n1", 8, 8), c("n1", 8, 0)}, statusD: 1, statC: 1,
		},
		{
			name:    "foreign container holds 3 of 8: new container takes the 5 free",
			in:      daemonsetInput{nodes: map[string]dsNode{"n1": sharedFree(8, 1000, 1000, 1000, 1000, 1000)}, cons: cons},
			want:    []dsWant{d("n1", 5, 5), c("n1", 10, hp(5000, 1, 10))},
			statusD: 1, statC: 1,
		},
		{
			name:    "the largest free drives feed the capacity",
			in:      daemonsetInput{nodes: map[string]dsNode{"n1": sharedFree(4, 4000, 3000, 2000, 1000)}, numDrivesPin: 2, cons: cons},
			want:    []dsWant{d("n1", 2, 2), c("n1", 4, hp(7000, 1, 4))},
			unused:  []string{"2 of 4 signed drives unused on n1 (numDrives pin=2)"},
			statusD: 1, statC: 1,
		},
		{
			name:   "every drive held: no container",
			in:     daemonsetInput{nodes: map[string]dsNode{"n1": sharedFree(8)}, cons: cons},
			noFree: []string{"n1 (8 signed, 8 held by other containers)"}, statusD: 1, statC: 1,
		},
		{
			name: "numDrives pin above the free drives is written",
			in:   daemonsetInput{nodes: map[string]dsNode{"n1": sharedFree(8, 1000, 1000, 1000)}, numDrivesPin: 10, cons: cons},
			want: []dsWant{d("n1", 10, 10), c("n1", 19, 0)}, capped: 1, statusD: 1, statC: 1,
		},
		{
			name: "blocked drive with no spare is lowered",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": sharedFree(7, 1000, 1000, 1000, 1000, 1000)},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(6, 6)}, map[string]*weka.WekaContainer{"n1": ctr(0, 12)}),
				cons:  cons,
			},
			want: []dsWant{d("n1", 5, 6), c("n1", 12, 0)}, statusD: 1, statC: 1,
		},
		{
			name: "drives taken before the claim lower the container, never below 1",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": sharedFree(8)},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(8, 8)}, map[string]*weka.WekaContainer{"n1": ctr(0, 16)}),
				cons:  cons,
			},
			want: []dsWant{d("n1", 1, 8), c("n1", 16, 0)}, statusD: 1, statC: 1,
			noFree: []string{"n1 (8 signed, 8 held by other containers; container waits for a free drive)"},
		},
		{
			name: "pinned container is not lowered",
			in: daemonsetInput{
				nodes:        map[string]dsNode{"n1": sharedFree(8, 1000, 1000, 1000)},
				live:         liveOf(map[string]*weka.WekaContainer{"n1": ctr(8, 8)}, map[string]*weka.WekaContainer{"n1": ctr(0, 16)}),
				numDrivesPin: 4,
				cons:         cons,
			},
			want: []dsWant{d("n1", 8, 8), c("n1", 16, 0)}, statusD: 1, statC: 1,
		},
		{
			name: "drives freed by another cluster are taken",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": sharedFree(8, drives(8)...)},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(3, 3)}, map[string]*weka.WekaContainer{"n1": ctr(0, 6)}),
				cons:  cons,
			},
			want: []dsWant{d("n1", 8, 8), c("n1", 16, hp(8000, 1, 16))}, statusD: 1, statC: 1,
		},
		{
			name: "node not listed keeps the container as is",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": {driveRole: true, computeRole: true, drivesGiB: drives(8)}},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(3, 3)}, map[string]*weka.WekaContainer{"n1": ctr(0, 6)}),
				cons:  cons,
			},
			want: []dsWant{d("n1", 3, 3), c("n1", 6, hp(3000, 1, 6))}, statusD: 1, statC: 1,
		},
		{
			name: "existing cores are never lowered",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": shared(8)},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(8, 10)}, map[string]*weka.WekaContainer{"n1": ctr(0, 30)}),
				cons:  cons,
			},
			// wants 16 compute cores, but the existing 30 already covers it: no cap warning
			want: []dsWant{d("n1", 8, 10), c("n1", 30, 0)}, statusD: 1, statC: 1,
		},
		{
			name: "cap is reported when the existing container stays below what was wanted",
			in: daemonsetInput{
				nodes: map[string]dsNode{"n1": shared(8)},
				live:  liveOf(map[string]*weka.WekaContainer{"n1": ctr(8, 10)}, map[string]*weka.WekaContainer{"n1": ctr(0, 10)}),
				cons:  cons,
			},
			want: []dsWant{d("n1", 8, 10), c("n1", 19, 0)}, capped: 1, statusD: 1, statC: 1,
		},
		{
			name: "numDrives pin below an existing container's drives reports nothing unused",
			in: daemonsetInput{
				nodes:        map[string]dsNode{"n1": shared(6)},
				live:         liveOf(map[string]*weka.WekaContainer{"n1": ctr(6, 6)}, nil),
				numDrivesPin: 4,
				cons:         cons,
			},
			want: []dsWant{d("n1", 6, 6), c("n1", 12, hp(6000, 1, 12))}, statusD: 1, statC: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planDaemonset(&tc.in)
			var got []dsWant
			for _, g := range p.targets {
				w := dsWant{mode: g.mode, node: g.node, drives: g.numDrives, cores: g.cores}
				for _, want := range tc.want {
					if want.mode == g.mode && want.node == g.node && want.hugepages != 0 {
						w.hugepages = g.computeHugepagesMiB
					}
				}
				got = append(got, w)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("targets\n got %v\nwant %v", got, tc.want)
			}
			if !slices.Equal(p.unsigned, tc.unsigned) {
				t.Errorf("unsigned = %v, want %v", p.unsigned, tc.unsigned)
			}
			if fmt.Sprint(p.ineligible) != fmt.Sprint(map[string]string(nilIfEmpty(tc.ineligible))) {
				t.Errorf("ineligible = %v, want %v", p.ineligible, tc.ineligible)
			}
			if !slices.Equal(p.unused, tc.unused) {
				t.Errorf("unused = %v, want %v", p.unused, tc.unused)
			}
			if !slices.Equal(p.noFree, tc.noFree) {
				t.Errorf("noFree = %v, want %v", p.noFree, tc.noFree)
			}
			if len(p.capped) != tc.capped {
				t.Errorf("capped = %v, want %d entries", p.capped, tc.capped)
			}
			if p.statusDesiredDrive != tc.statusD || p.statusDesiredCompute != tc.statC {
				t.Errorf("status desired = %d/%d, want %d/%d", p.statusDesiredDrive, p.statusDesiredCompute, tc.statusD, tc.statC)
			}
		})
	}
}

func nilIfEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func TestPlanDaemonsetPairedFlag(t *testing.T) {
	p := planDaemonset(&daemonsetInput{nodes: map[string]dsNode{"n1": shared(4), "n2": driveOnly(4), "x1": computeOnly()}, cons: dsCons(19, 2)})
	paired := map[string]bool{}
	for _, tg := range p.targets {
		paired[tg.mode+"/"+tg.node] = tg.paired
	}
	want := map[string]bool{"drive/n1": true, "drive/n2": false, "compute/n1": true, "compute/x1": false}
	if fmt.Sprint(paired) != fmt.Sprint(want) {
		t.Errorf("paired = %v, want %v", paired, want)
	}
}

// dsGlueLoop builds a cluster that acts as a daemonset over nodes labeled t=1, with the given drive counts.
func dsGlueLoop(t *testing.T, nodeDrives map[string]int, formed bool, containers ...*weka.WekaContainer) *wekaClusterReconcilerLoop {
	t.Helper()
	prevMax, prevRatio := globalconfig.Config.CapacityPlanner.MaxCoresPerContainer, globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio
	globalconfig.Config.CapacityPlanner.MaxCoresPerContainer, globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio = 19, 2.0
	t.Cleanup(func() {
		globalconfig.Config.CapacityPlanner.MaxCoresPerContainer, globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio = prevMax, prevRatio
	})

	cluster := testClusterFor(t)
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}
	cluster.Spec.NodeSelector = map[string]string{"t": "1"}
	if formed {
		cluster.Status.Conditions = []metav1.Condition{{Type: condition.CondClusterCreated, Status: metav1.ConditionTrue}}
	}
	loop := newUpgradeLoop(t, cluster, containers)
	for name, n := range nodeDrives {
		var entries []domain.DriveEntry
		for i := 0; i < n; i++ {
			entries = append(entries, domain.DriveEntry{Serial: fmt.Sprintf("%s-%d", name, i), CapacityGiB: 1000})
		}
		ann, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: name, Labels: map[string]string{"t": "1"}, Annotations: map[string]string{consts.AnnotationWekaFullDrives: string(ann)},
		}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		if err := loop.getClient().Create(context.Background(), node); err != nil {
			t.Fatal(err)
		}
	}
	return loop
}

func TestBuildMissingContainersDaemonsetCreatesPinnedContainers(t *testing.T) {
	loop := dsGlueLoop(t, map[string]int{"n1": 4, "n2": 4}, true)
	loop.cluster.Spec.Dynamic.ComputeHugepages = 30000

	built, err := loop.BuildMissingContainers(context.Background())
	if err != nil {
		t.Fatalf("BuildMissingContainers: %v", err)
	}
	type shape struct{ drives, cores, hugepages int }
	got := map[string]shape{}
	for _, c := range built {
		got[c.Spec.Mode+"/"+string(c.Spec.NodeAffinity)] = shape{c.Spec.NumDrives, c.Spec.NumCores, c.Spec.Hugepages}
	}
	want := map[string]shape{
		"drive/n1": {4, 4, got["drive/n1"].hugepages}, "drive/n2": {4, 4, got["drive/n2"].hugepages},
		"compute/n1": {0, 8, 30000}, "compute/n2": {0, 8, 30000},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("built = %v, want %v", got, want)
	}
	if got["drive/n1"].hugepages == 0 {
		t.Error("drive hugepages not set")
	}
}

func TestBuildMissingContainersDaemonsetRaisesExistingAndEmitsGrowth(t *testing.T) {
	drive := growableAutoFullDrivesDriveContainer("drive-n1", "n1", 2, 2)
	loop := dsGlueLoop(t, map[string]int{"n1": 4}, true, drive)

	if _, err := loop.BuildMissingContainers(context.Background()); err != nil {
		t.Fatalf("BuildMissingContainers: %v", err)
	}
	got := &weka.WekaContainer{}
	if err := loop.getClient().Get(context.Background(), client.ObjectKeyFromObject(drive), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.NumDrives != 4 || got.Spec.NumCores != 4 || got.Spec.Hugepages == 0 {
		t.Errorf("drive = %d drives/%d cores/%d hugepages, want 4/4/>0", got.Spec.NumDrives, got.Spec.NumCores, got.Spec.Hugepages)
	}
	evs := drainLoopEvents(t, loop)
	if len(eventsMatching(evs, "Warning AutoFullDrivesResized")) != 1 || len(eventsMatching(evs, "Normal AutoFullDrivesResized")) != 1 {
		t.Errorf("want one container and one cluster AutoFullDrivesResized event, got %v", evs)
	}
}

func TestBuildMissingContainersDaemonsetPreFormationCapTakesPairedFirst(t *testing.T) {
	prev := globalconfig.Consts.FormClusterMaxDriveContainers
	globalconfig.Consts.FormClusterMaxDriveContainers = 1
	t.Cleanup(func() { globalconfig.Consts.FormClusterMaxDriveContainers = prev })

	loop := dsGlueLoop(t, map[string]int{"a": 4, "b": 4}, false)
	// "a" is drive-only, "b" is both: only "b" is paired.
	loop.cluster.Spec.RoleNodeSelector.Compute = &map[string]string{"t": "1", "c": "1"}
	b := &corev1.Node{}
	if err := loop.getClient().Get(context.Background(), client.ObjectKey{Name: "b"}, b); err != nil {
		t.Fatal(err)
	}
	b.Labels["c"] = "1"
	if err := loop.getClient().Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}

	built, err := loop.BuildMissingContainers(context.Background())
	if err != nil {
		t.Fatalf("BuildMissingContainers: %v", err)
	}
	var drives []string
	for _, c := range built {
		if c.Spec.Mode == weka.WekaContainerModeDrive {
			drives = append(drives, string(c.Spec.NodeAffinity))
		}
	}
	if strings.Join(drives, ",") != "b" {
		t.Errorf("drive containers created on %v, want only the paired node b", drives)
	}
}

func TestRaiseDaemonsetContainerHugepagesFollowAuto(t *testing.T) {
	ptr := func(i int) *int { return &i }
	for _, mode := range []string{weka.WekaContainerModeCompute, weka.WekaContainerModeDrive} {
		for _, tc := range []struct {
			name      string
			pin       *int // offset from auto; nil = unpinned
			cur, want int  // offsets from auto
			warn      bool
		}{
			{name: "unpinned lowered to auto", cur: 500, want: 0},
			{name: "unpinned raised to auto", cur: -500, want: 0},
			{name: "pin above auto is written", pin: ptr(100), cur: -500, want: 100},
			{name: "pin below auto lowers current to auto", pin: ptr(-100), cur: 300, want: 0, warn: true},
			{name: "pin below auto raises current below auto", pin: ptr(-100), cur: -500, want: 0, warn: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				c := growableAutoFullDrivesDriveContainer("c1", "n1", 4, 4)
				c.Spec.Mode = mode
				loop := dsGlueLoop(t, nil, true, c)
				target := dsContainerTarget{node: "n1", mode: mode, numDrives: 4, cores: 4, computeHugepagesMiB: 15000}
				auto := autoHugepagesFor(loop.cluster, &target, 4, 4).Hugepages
				c.Spec.Hugepages = auto + tc.cur
				if err := loop.getClient().Update(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				if tc.pin != nil {
					if mode == weka.WekaContainerModeDrive {
						loop.cluster.Spec.Dynamic.DriveHugepages = auto + *tc.pin
					} else {
						loop.cluster.Spec.Dynamic.ComputeHugepages = auto + *tc.pin
					}
				}
				in, err := loop.collectDaemonsetInput(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				_, below, err := loop.raiseDaemonsetContainer(context.Background(), c, &target, in.hugepagesPin[mode], in.hugepagesOffsetPin[mode])
				if err != nil {
					t.Fatal(err)
				}
				got := &weka.WekaContainer{}
				if err := loop.getClient().Get(context.Background(), client.ObjectKeyFromObject(c), got); err != nil {
					t.Fatal(err)
				}
				if got.Spec.Hugepages != auto+tc.want {
					t.Errorf("hugepages = %d, want %d", got.Spec.Hugepages, auto+tc.want)
				}
				wantFrag := fmt.Sprintf("c1/hugepages pin=%d auto=%d current=%d", auto+valueOr(tc.pin), auto, auto+tc.cur)
				if tc.warn != (len(below) == 1 && below[0] == wantFrag) || (!tc.warn && len(below) != 0) {
					t.Errorf("below = %v, want [%s] = %v", below, wantFrag, tc.warn)
				}
				if tc.warn != (len(eventsMatching(drainLoopEvents(t, loop), "Warning "+reasonAutoFullDrivesHugepagesPinBelowAuto)) == 1) {
					t.Errorf("container warning emitted != %v", tc.warn)
				}
			})
		}
	}
}

func valueOr(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func TestRaiseDaemonsetContainerOffsetFollowsAuto(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pin       *int // offset from auto offset; nil = unpinned
		cur, want int
		warn      bool
	}{
		{name: "unpinned lowered to auto", cur: 50, want: 0},
		{name: "unpinned raised to auto", cur: -5, want: 0},
		{name: "pin above auto is written", pin: new(10), cur: -5, want: 10},
		{name: "pin below auto lowers current to auto", pin: new(-10), cur: 5, want: 0, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := growableAutoFullDrivesDriveContainer("c1", "n1", 4, 4)
			c.Spec.Mode = weka.WekaContainerModeCompute
			loop := dsGlueLoop(t, nil, true, c)
			target := dsContainerTarget{node: "n1", mode: c.Spec.Mode, cores: 4, computeHugepagesMiB: 15000}
			auto := autoHugepagesFor(loop.cluster, &target, 4, 0)
			c.Spec.Hugepages = auto.Hugepages
			c.Spec.HugepagesOffset = auto.HugepagesOffset + tc.cur
			if err := loop.getClient().Update(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if tc.pin != nil {
				loop.cluster.Spec.Dynamic.ComputeHugepagesOffset = auto.HugepagesOffset + *tc.pin
			}
			in, err := loop.collectDaemonsetInput(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, below, err := loop.raiseDaemonsetContainer(context.Background(), c, &target, in.hugepagesPin[c.Spec.Mode], in.hugepagesOffsetPin[c.Spec.Mode])
			if err != nil {
				t.Fatal(err)
			}
			got := &weka.WekaContainer{}
			if err := loop.getClient().Get(context.Background(), client.ObjectKeyFromObject(c), got); err != nil {
				t.Fatal(err)
			}
			if got.Spec.HugepagesOffset != auto.HugepagesOffset+tc.want {
				t.Errorf("offset = %d, want %d", got.Spec.HugepagesOffset, auto.HugepagesOffset+tc.want)
			}
			if tc.warn != (len(below) == 1 && strings.HasPrefix(below[0], "c1/hugepagesOffset pin=")) {
				t.Errorf("below = %v, want warning = %v", below, tc.warn)
			}
		})
	}
}

func TestBuildMissingContainersDaemonsetCreateWithPinBelowAutoUsesAuto(t *testing.T) {
	loop := dsGlueLoop(t, map[string]int{"n1": 4}, true)
	loop.cluster.Spec.Dynamic.ComputeHugepages = 1000
	loop.cluster.Spec.Dynamic.ComputeHugepagesOffset = 1

	built, err := loop.BuildMissingContainers(context.Background())
	if err != nil {
		t.Fatalf("BuildMissingContainers: %v", err)
	}
	in, err := loop.collectDaemonsetInput(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var auto allocator.ContainerHugepages
	for _, tg := range planDaemonset(in).targets {
		if tg.mode == weka.WekaContainerModeCompute {
			auto = autoHugepagesFor(loop.cluster, &tg, tg.cores, 0)
		}
	}
	for _, c := range built {
		if c.Spec.Mode == weka.WekaContainerModeCompute && (c.Spec.Hugepages != auto.Hugepages || c.Spec.HugepagesOffset != auto.HugepagesOffset) {
			t.Errorf("compute hugepages/offset = %d/%d, want auto %d/%d", c.Spec.Hugepages, c.Spec.HugepagesOffset, auto.Hugepages, auto.HugepagesOffset)
		}
	}
	if len(eventsMatching(drainLoopEvents(t, loop), "Warning "+reasonAutoFullDrivesHugepagesPinBelowAuto)) != 1 {
		t.Error("want one cluster warning for the pins below auto")
	}
}

func TestBuildMissingContainersDaemonsetFailedRaiseDoesNotBlockCreation(t *testing.T) {
	drive := growableAutoFullDrivesDriveContainer("drive-n1", "n1", 2, 2)
	loop := dsGlueLoop(t, map[string]int{"n1": 4, "n2": 4}, true, drive)
	// Present in the cache but gone from the API, so raising it fails.
	if err := loop.getClient().Delete(context.Background(), drive); err != nil {
		t.Fatal(err)
	}

	built, err := loop.BuildMissingContainers(context.Background())
	if err == nil {
		t.Fatal("want the failed raise returned")
	}
	var onN2 int
	for _, c := range built {
		if c.Spec.NodeAffinity == "n2" {
			onN2++
		}
	}
	if onN2 != 2 {
		t.Errorf("built %d containers on n2, want its drive and compute", onN2)
	}
}

func TestBuildMissingContainersDaemonsetWritesStatusDesired(t *testing.T) {
	loop := dsGlueLoop(t, map[string]int{"n1": 4, "n2": 2}, true)
	if _, err := loop.BuildMissingContainers(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := loop.cluster.Status.Stats
	if st == nil || st.Containers.Drive.Containers.Desired != 2 || st.Containers.Compute.Containers.Desired != 2 || st.Drives.DriveCounters.Desired != 6 {
		t.Errorf("desired drive/compute containers, drives = %+v, want 2/2/6", st)
	}
}

func TestCollectDaemonsetInputTreatsUnparsableAnnotationAsUnsigned(t *testing.T) {
	loop := dsGlueLoop(t, map[string]int{"good": 4}, true)
	bad := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "bad", Labels: map[string]string{"t": "1"}, Annotations: map[string]string{consts.AnnotationWekaFullDrives: "{not json"},
	}}
	if err := loop.getClient().Create(context.Background(), bad); err != nil {
		t.Fatal(err)
	}
	in, err := loop.collectDaemonsetInput(context.Background())
	if err != nil {
		t.Fatalf("collectDaemonsetInput: %v", err)
	}
	p := planDaemonset(in)
	if len(p.unsigned) != 1 || !strings.HasPrefix(p.unsigned[0], "bad (unparsable annotation: ") {
		t.Errorf("unsigned = %v, want one entry for bad naming the parse error", p.unsigned)
	}
	for _, tg := range p.targets {
		if tg.node == "bad" {
			t.Errorf("unexpected target on the unsigned node: %+v", tg)
		}
	}
}

func driveCtr(name string, spec int, serials ...string) *weka.WekaContainer {
	c := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{domain.WekaLabelMode: weka.WekaContainerModeDrive}}}
	c.Spec.Mode, c.Spec.NumDrives, c.Spec.NumCores, c.Spec.NodeAffinity = weka.WekaContainerModeDrive, spec, spec, "n1"
	if len(serials) > 0 {
		c.Status.Allocations = &weka.ContainerAllocations{Drives: serials}
	}
	return c
}

func aged(c *weka.WekaContainer, sec int64) *weka.WekaContainer {
	c.CreationTimestamp = metav1.Unix(sec, 0)
	return c
}

func deletingCtr(c *weka.WekaContainer) *weka.WekaContainer {
	c.Spec.State = weka.ContainerStateDeleting
	return c
}

func TestCollectDaemonsetInputFreeDrives(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		own        *weka.WekaContainer
		foreign    []*weka.WekaContainer
		wantListed bool
		wantAvail  int
	}{
		{name: "no container yet: foreign holds 2, another reserves 1", foreign: []*weka.WekaContainer{driveCtr("other", 2, "n1-0", "n1-1"), driveCtr("reserved", 1)}, wantListed: true, wantAvail: 1},
		{name: "own holds 2, nothing foreign: the rest is free", own: driveCtr("own", 2, "n1-0", "n1-1"), wantListed: true, wantAvail: 4},
		{name: "own holds every signed drive: not listed", own: driveCtr("own", 4, "n1-0", "n1-1", "n1-2", "n1-3")},
		{name: "own asks for more than it holds", own: driveCtr("own", 4, "n1-0", "n1-1"), foreign: []*weka.WekaContainer{driveCtr("other", 2, "n1-2", "n1-3")}, wantListed: true, wantAvail: 2},
		{name: "foreign holding 1 of 3 still reserves the other 2", foreign: []*weka.WekaContainer{driveCtr("partial", 3, "n1-0")}, wantListed: true, wantAvail: 1},
		{name: "deleting foreign reserves nothing but its serials stay held", foreign: []*weka.WekaContainer{deletingCtr(driveCtr("going", 3, "n1-0"))}, wantListed: true, wantAvail: 3},
		{name: "older foreign claim reserves against ours", own: aged(driveCtr("own", 4), 10), foreign: []*weka.WekaContainer{aged(driveCtr("first", 2), 5)}, wantListed: true, wantAvail: 2},
		{name: "newer foreign claim does not reserve against ours", own: aged(driveCtr("own", 4), 10), foreign: []*weka.WekaContainer{aged(driveCtr("later", 2), 20)}, wantListed: true, wantAvail: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var own []*weka.WekaContainer
			if tc.own != nil {
				own = append(own, tc.own)
			}
			loop := dsGlueLoop(t, map[string]int{"n1": 4}, true, own...)
			for _, f := range tc.foreign {
				if err := loop.getClient().Create(ctx, f); err != nil {
					t.Fatal(err)
				}
			}
			in, err := loop.collectDaemonsetInput(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n := in.nodes["n1"]; n.listed != tc.wantListed || len(n.availGiB) != tc.wantAvail {
				t.Errorf("listed=%v avail=%d, want %v/%d", n.listed, len(n.availGiB), tc.wantListed, tc.wantAvail)
			}
		})
	}
}

func TestBuildDaemonsetContainersLowersNumDrives(t *testing.T) {
	ctx := context.Background()
	own := aged(driveCtr("own", 4), 10)
	loop := dsGlueLoop(t, map[string]int{"n1": 4}, true, own)
	for _, f := range []*weka.WekaContainer{driveCtr("other", 2, "n1-0", "n1-1"), aged(driveCtr("reserved", 1), 5)} {
		if err := loop.getClient().Create(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := loop.BuildMissingContainers(ctx); err != nil {
		t.Fatal(err)
	}
	got := &weka.WekaContainer{}
	if err := loop.getClient().Get(ctx, client.ObjectKeyFromObject(own), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.NumDrives != 1 {
		t.Errorf("numDrives = %d, want 1 (4 signed - 2 held - 1 reserved)", got.Spec.NumDrives)
	}
}

func TestBuildDaemonsetContainersNoFreeDrivesEmitsEvent(t *testing.T) {
	loop := dsGlueLoop(t, map[string]int{"n1": 2}, true)
	if err := loop.getClient().Create(context.Background(), driveCtr("other", 2, "n1-0", "n1-1")); err != nil {
		t.Fatal(err)
	}
	built, err := loop.BuildMissingContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 0 {
		t.Errorf("built %d containers on a node with no free drives", len(built))
	}
	if evs := eventsMatching(drainLoopEvents(t, loop), "AutoFullDrivesNoFreeDrives"); len(evs) != 1 || !strings.Contains(evs[0], "n1 (2 signed, 2 held by other containers)") {
		t.Errorf("want one NoFreeDrives event naming n1, got %v", evs)
	}
}

func TestCollectDaemonsetInputListsOnlyNodesWhereAnswerCanChange(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		own  []*weka.WekaContainer
		want bool
	}{
		{name: "own holds every signed drive: not listed", own: []*weka.WekaContainer{driveCtr("own", 4, "n1-0", "n1-1", "n1-2", "n1-3")}},
		{name: "own holds fewer than signed: listed every pass", own: []*weka.WekaContainer{driveCtr("own", 2, "n1-0", "n1-1")}, want: true},
		{name: "own holds nothing: listed every pass", own: []*weka.WekaContainer{driveCtr("own", 2)}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop := dsGlueLoop(t, map[string]int{"n1": 4}, true, tc.own...)
			for pass := 0; pass < 2; pass++ {
				in, err := loop.collectDaemonsetInput(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if got := in.nodes["n1"].listed; got != tc.want {
					t.Errorf("pass %d: listed=%v, want %v", pass, got, tc.want)
				}
			}
		})
	}
}

func TestPlanDaemonsetExistingContainerCapacity(t *testing.T) {
	cons := dsCons(19, 2)
	entries := []domain.DriveEntry{{Serial: "a", CapacityGiB: 4000}, {Serial: "b", CapacityGiB: 1000}, {Serial: "c", CapacityGiB: 1000}, {Serial: "d", CapacityGiB: 500}}
	own := func(serials ...string) *weka.WekaContainer {
		c := ctr(2, 2)
		c.Status.Allocations = &weka.ContainerAllocations{Drives: serials}
		return c
	}
	node := func(listed bool, avail ...int) dsNode {
		return dsNode{driveRole: true, drivesGiB: []int{4000, 1000, 1000, 500}, entries: entries, listed: listed, availGiB: avail}
	}
	tlc := func(n dsNode, c *weka.WekaContainer) (int, int) {
		p := planDaemonset(&daemonsetInput{nodes: map[string]dsNode{"n1": n}, cons: cons,
			live: liveOf(map[string]*weka.WekaContainer{"n1": c}, nil)})
		return p.targets[0].tlcGiB, p.targets[0].numDrives
	}
	// Holds the 1000 GiB drive "b" and needs one more: the largest free one, whether or not the node was listed.
	listedTlc, _ := tlc(node(true, 4000, 1000), own("b"))
	unlistedTlc, _ := tlc(node(false), own("b"))
	if listedTlc != 5000 || unlistedTlc != 5000 {
		t.Errorf("tlc listed=%d unlisted=%d, want 5000 both", listedTlc, unlistedTlc)
	}

	// A serial no longer in the signed, unblocked entries keeps numDrives from being lowered.
	if _, drives := tlc(node(true, 4000, 1000), own("b", "gone")); drives != 2 {
		t.Errorf("numDrives = %d, want 2 while a blocked serial is still allocated", drives)
	}
	if _, drives := tlc(node(true, 1000, 1000), own("b", "c")); drives != 2 {
		t.Errorf("numDrives = %d, want 2 (holds both)", drives)
	}
	if _, drives := tlc(node(true, 1000), own("b")); drives != 1 {
		t.Errorf("numDrives = %d, want 1 (lowered to what the node can give)", drives)
	}
}
