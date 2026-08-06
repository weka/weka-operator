package operations

import (
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

// drv is a small fixture helper for building a weka.Drive with just the fields the gate cares about.
func drv(serial, status string) weka.Drive {
	return weka.Drive{SerialNumber: serial, Status: status}
}

// healthyStatus is a fully-healthy baseline WekaStatusResponse; individual tests mutate one field
// at a time off of this to isolate what they're checking. Returns a fresh pointer per call, so
// mutating one test's copy can't leak into another's.
func healthyStatus() *services.WekaStatusResponse {
	return &services.WekaStatusResponse{
		Status: "OK",
		Drives: services.WekaStatusObjectCounter{Active: 10, Total: 10},
		Containers: services.WekaStatusContainers{
			Drives:   services.WekaStatusObjectCounter{Active: 10, Total: 10},
			Computes: services.WekaStatusObjectCounter{Active: 10, Total: 10},
		},
		Rebuild: services.WekaStatusRebuild{},
	}
}

func TestRebuildIsFullyProtected(t *testing.T) {
	t.Run("empty ProtectionState reads as fully protected", func(t *testing.T) {
		// This is a deliberate fail-open default for an empty/nil slice: callers must never represent
		// a GetWekaStatus fetch error as an empty ProtectionState, or this trap will wave it through.
		rebuild := services.WekaStatusRebuild{}
		if !rebuild.IsFullyProtected() {
			t.Errorf("IsFullyProtected() on empty ProtectionState = false, want true")
		}
	})

	t.Run("a failing protection state blocks", func(t *testing.T) {
		rebuild := services.WekaStatusRebuild{
			ProtectionState: []services.ProtectionState{{NumFailures: 2, Percent: 3.4}},
		}
		if rebuild.IsFullyProtected() {
			t.Errorf("IsFullyProtected() with Percent>0 && NumFailures>0 = true, want false")
		}
	})
}

func TestProtectionStateSummary_PicksWorst(t *testing.T) {
	t.Run("worst of several failing states", func(t *testing.T) {
		states := []services.ProtectionState{
			{NumFailures: 1, Percent: 1.0},
			{NumFailures: 5, Percent: 9.9},  // worst: highest percent among the failing states
			{NumFailures: 0, Percent: 50.0}, // NumFailures<=0 must be ignored despite the highest percent
			{NumFailures: 2, Percent: 3.4},
		}
		want := "5 failures @ 9.9%"
		if got := protectionStateSummary(states); got != want {
			t.Errorf("protectionStateSummary() = %q, want %q", got, want)
		}
	})

	t.Run("nil -> unknown protection state", func(t *testing.T) {
		if got := protectionStateSummary(nil); got != "unknown protection state" {
			t.Errorf("protectionStateSummary(nil) = %q, want %q", got, "unknown protection state")
		}
	})

	t.Run("no failing states -> unknown protection state", func(t *testing.T) {
		states := []services.ProtectionState{{NumFailures: 0, Percent: 20}}
		if got := protectionStateSummary(states); got != "unknown protection state" {
			t.Errorf("protectionStateSummary(no failures) = %q, want %q", got, "unknown protection state")
		}
	})
}

func TestClusterStatusReason(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{"OK passes", "OK", ""},
		{"REDISTRIBUTING passes", "REDISTRIBUTING", ""},
		{"anything else blocks", "DEGRADED", `weka status is "DEGRADED" (want OK or REDISTRIBUTING)`},
		{"empty status blocks", "", `weka status is "" (want OK or REDISTRIBUTING)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterStatusReason(tc.status); got != tc.want {
				t.Errorf("clusterStatusReason(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestDriveHealthReason(t *testing.T) {
	t.Run("active == total -> no reason", func(t *testing.T) {
		if got := driveHealthReason(4, 4, nil); got != "" {
			t.Errorf("driveHealthReason() = %q, want empty", got)
		}
	})

	t.Run("shortfall with drive detail -> exact design-doc format", func(t *testing.T) {
		drives := []weka.Drive{
			drv("sn-9012", services.DriveStatusInactive),
			drv("sn-1234", services.DriveStatusInactive),
			drv("sn-5678", services.DriveStatusInactive),
		}
		want := "3 drives not ACTIVE (INACTIVE: sn-1234, sn-5678, sn-9012)"
		if got := driveHealthReason(0, 3, drives); got != want {
			t.Errorf("driveHealthReason() = %q, want %q", got, want)
		}
	})

	t.Run("shortfall with no drive detail available", func(t *testing.T) {
		want := "1 drives not ACTIVE (no drive detail available)"
		if got := driveHealthReason(2, 3, nil); got != want {
			t.Errorf("driveHealthReason() = %q, want %q", got, want)
		}
	})

	t.Run("PHASING_IN also blocks", func(t *testing.T) {
		drives := []weka.Drive{drv("sn-4321", services.DriveStatusPhasingIn)}
		want := "1 drives not ACTIVE (PHASING_IN: sn-4321)"
		if got := driveHealthReason(0, 1, drives); got != want {
			t.Errorf("driveHealthReason() = %q, want %q", got, want)
		}
	})

	t.Run("unknown/arbitrary status also blocks (allow-list, not deny-list)", func(t *testing.T) {
		// Any status but ACTIVE -- including one this repo has no constant for -- must read unhealthy.
		// See the FAILED case below for why that is not hypothetical.
		drives := []weka.Drive{drv("sn-unknown", "SOME_UNEXPECTED_STATUS")}
		want := "1 drives not ACTIVE (SOME_UNEXPECTED_STATUS: sn-unknown)"
		if got := driveHealthReason(0, 1, drives); got != want {
			t.Errorf("driveHealthReason() = %q, want %q", got, want)
		}
	})

	// Real serials and status observed live while this repo declared no FAILED constant at all. A
	// deny-list of "known bad" statuses would have scored these healthy and opened the gate on a tenant
	// that had genuinely lost drives. Keep the check an allow-list.
	t.Run("FAILED blocks — the status that proved the set is open", func(t *testing.T) {
		drives := []weka.Drive{
			drv("23164A39D558", services.DriveStatusFailed),
			drv("23164A39D57B", services.DriveStatusFailed),
		}
		want := "2 drives not ACTIVE (FAILED: 23164A39D558, 23164A39D57B)"
		if got := driveHealthReason(0, 2, drives); got != want {
			t.Errorf("driveHealthReason() = %q, want %q", got, want)
		}
	})

	t.Run("FAILED mixed with ACTIVE still blocks and names only the offender", func(t *testing.T) {
		drives := []weka.Drive{
			drv("sn-ok-1", services.DriveStatusActive),
			drv("23164A39D558", services.DriveStatusFailed),
			drv("sn-ok-2", services.DriveStatusActive),
		}
		want := "1 drives not ACTIVE (FAILED: 23164A39D558)"
		if got := driveHealthReason(2, 3, drives); got != want {
			t.Errorf("driveHealthReason() = %q, want %q", got, want)
		}
	})
}

// Guards the allow-list invariant itself: whatever status weka invents, a drive that is not ACTIVE must
// block. If someone "tidies" driveHealthReason into a deny-list of INACTIVE/PHASING_IN/FAILED, the
// unknown-status subtest fails.
func TestDriveStatusAllowListIsExhaustiveOverACTIVEOnly(t *testing.T) {
	for _, status := range []string{
		services.DriveStatusInactive,
		services.DriveStatusPhasingIn,
		services.DriveStatusFailed,
		"SOME_STATUS_WEKA_ADDS_LATER",
	} {
		t.Run(status, func(t *testing.T) {
			if got := driveHealthReason(0, 1, []weka.Drive{drv("sn-1", status)}); got == "" {
				t.Errorf("status %q was treated as healthy; the check must allow-list ACTIVE only", status)
			}
		})
	}

	t.Run("ACTIVE is the only status that passes", func(t *testing.T) {
		if got := driveHealthReason(1, 1, []weka.Drive{drv("sn-1", services.DriveStatusActive)}); got != "" {
			t.Errorf("driveHealthReason() = %q, want empty for an all-ACTIVE cluster", got)
		}
	})
}

func TestUnhealthyDriveDetail(t *testing.T) {
	t.Run("no unhealthy drives -> empty", func(t *testing.T) {
		drives := []weka.Drive{drv("sn-1", services.DriveStatusActive)}
		if got := unhealthyDriveDetail(drives); got != "" {
			t.Errorf("unhealthyDriveDetail() = %q, want empty", got)
		}
	})

	t.Run("groups by status, sorts serials within a group, excludes ACTIVE", func(t *testing.T) {
		drives := []weka.Drive{
			drv("sn-9012", services.DriveStatusInactive),
			drv("sn-active", services.DriveStatusActive),
			drv("sn-1234", services.DriveStatusInactive),
			drv("sn-4321", services.DriveStatusPhasingIn),
			drv("sn-5678", services.DriveStatusInactive),
		}
		want := "INACTIVE: sn-1234, sn-5678, sn-9012; PHASING_IN: sn-4321"
		if got := unhealthyDriveDetail(drives); got != want {
			t.Errorf("unhealthyDriveDetail() = %q, want %q", got, want)
		}
	})

	t.Run("deterministic regardless of input order", func(t *testing.T) {
		a := []weka.Drive{
			drv("sn-9012", services.DriveStatusInactive),
			drv("sn-4321", services.DriveStatusPhasingIn),
			drv("sn-1234", services.DriveStatusInactive),
			drv("sn-5678", services.DriveStatusInactive),
		}
		b := []weka.Drive{
			drv("sn-4321", services.DriveStatusPhasingIn),
			drv("sn-5678", services.DriveStatusInactive),
			drv("sn-1234", services.DriveStatusInactive),
			drv("sn-9012", services.DriveStatusInactive),
		}
		gotA := unhealthyDriveDetail(a)
		gotB := unhealthyDriveDetail(b)
		if gotA != gotB {
			t.Errorf("same drives reordered produced different output: %q vs %q", gotA, gotB)
		}
	})

	t.Run("falls back to UUID when serial number is empty", func(t *testing.T) {
		drives := []weka.Drive{{Uuid: "uuid-1", Status: services.DriveStatusInactive}}
		want := "INACTIVE: uuid-1"
		if got := unhealthyDriveDetail(drives); got != want {
			t.Errorf("unhealthyDriveDetail() = %q, want %q", got, want)
		}
	})

	t.Run("caps serials per status at the const and appends a count of the rest", func(t *testing.T) {
		var drives []weka.Drive
		for i := 0; i < maxUnhealthyDriveSerialsPerStatus+3; i++ {
			drives = append(drives, drv(fmt.Sprintf("sn-%02d", i), services.DriveStatusInactive))
		}
		got := unhealthyDriveDetail(drives)
		if !strings.HasPrefix(got, "INACTIVE: ") {
			t.Fatalf("unhealthyDriveDetail() = %q, want it to start with the INACTIVE group", got)
		}
		if !strings.HasSuffix(got, "… and 3 more") {
			t.Errorf("unhealthyDriveDetail() = %q, want it to end with the overflow count", got)
		}
		shown := strings.Count(got, "sn-")
		if shown != maxUnhealthyDriveSerialsPerStatus {
			t.Errorf("unhealthyDriveDetail() listed %d serials, want exactly the cap of %d", shown, maxUnhealthyDriveSerialsPerStatus)
		}
	})

	t.Run("at exactly the cap, no overflow suffix is added", func(t *testing.T) {
		var drives []weka.Drive
		for i := 0; i < maxUnhealthyDriveSerialsPerStatus; i++ {
			drives = append(drives, drv(fmt.Sprintf("sn-%02d", i), services.DriveStatusInactive))
		}
		got := unhealthyDriveDetail(drives)
		if strings.Contains(got, "more") {
			t.Errorf("unhealthyDriveDetail() = %q, want no overflow suffix exactly at the cap", got)
		}
	})
}

func TestContainerThresholdReason(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		counter   services.WekaStatusObjectCounter
		expected  int
		threshold int
		want      string
	}{
		{"no containers of this kind -> no reason", "drive", services.WekaStatusObjectCounter{Active: 0, Total: 0}, 0, 80, ""},
		{"exactly at threshold passes", "drive", services.WekaStatusObjectCounter{Active: 8, Total: 10}, 10, 80, ""},
		{"just below threshold blocks", "drive", services.WekaStatusObjectCounter{Active: 7, Total: 10}, 10, 80, "only 70% of drive containers active (threshold 80%)"},
		{"well below threshold blocks", "compute", services.WekaStatusObjectCounter{Active: 2, Total: 10}, 10, 80, "only 20% of compute containers active (threshold 80%)"},
		{"all active passes", "compute", services.WekaStatusObjectCounter{Active: 10, Total: 10}, 10, 100, ""},

		// The denominator is max(weka-reported, operator-expected), so neither side can shrink the bar.
		{
			"vanished containers still count against the threshold",
			"drive",
			services.WekaStatusObjectCounter{Active: 7, Total: 7}, // 3 of 10 gone: weka reports 7/7
			10,
			80,
			"only 70% of drive containers active (threshold 80%)",
		},
		{
			"stale operator list cannot shrink the bar below weka's count",
			"drive",
			services.WekaStatusObjectCounter{Active: 7, Total: 10},
			2, // operator only knows about 2
			80,
			"only 70% of drive containers active (threshold 80%)",
		},
		{
			"expected zero falls back to weka's count",
			"compute",
			services.WekaStatusObjectCounter{Active: 2, Total: 10},
			0,
			80,
			"only 20% of compute containers active (threshold 80%)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containerThresholdReason(tc.kind, tc.counter, tc.expected, tc.threshold); got != tc.want {
				t.Errorf("containerThresholdReason(%q, %+v, expected=%d, %d) = %q, want %q",
					tc.kind, tc.counter, tc.expected, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestExpectedContainers(t *testing.T) {
	ctr := func(mode string) *weka.WekaContainer {
		return &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{domain.WekaLabelMode: mode}},
		}
	}
	drive, compute := expectedContainers([]*weka.WekaContainer{
		ctr(weka.WekaContainerModeDrive),
		ctr(weka.WekaContainerModeDrive),
		ctr(weka.WekaContainerModeCompute),
		ctr(weka.WekaContainerModeSSDProxy), // neither drive nor compute
		{ObjectMeta: metav1.ObjectMeta{}},   // unlabelled
	})
	if drive != 2 || compute != 1 {
		t.Errorf("expectedContainers() = (drive=%d, compute=%d), want (2, 1)", drive, compute)
	}
	if d, c := expectedContainers(nil); d != 0 || c != 0 {
		t.Errorf("expectedContainers(nil) = (%d, %d), want (0, 0)", d, c)
	}
}

func TestSortedClusters_Deterministic(t *testing.T) {
	m := map[types.UID]*weka.WekaCluster{
		types.UID("uid-c"): {ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "cluster-a", UID: types.UID("uid-c")}},
		types.UID("uid-a"): {ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "cluster-z", UID: types.UID("uid-a")}},
		types.UID("uid-b"): {ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "cluster-a", UID: types.UID("uid-b")}},
	}
	wantOrder := []string{"ns-a/cluster-a", "ns-a/cluster-z", "ns-b/cluster-a"}

	// Go's map iteration order is randomized per run; calling this repeatedly on the same map is the
	// closest a single test binary run can get to exercising that randomness and still asserting a
	// stable, sorted result every time.
	for i := 0; i < 20; i++ {
		got := sortedClusters(m)
		if len(got) != len(wantOrder) {
			t.Fatalf("iteration %d: got %d clusters, want %d", i, len(got), len(wantOrder))
		}
		for j, c := range got {
			key := c.Namespace + "/" + c.Name
			if key != wantOrder[j] {
				t.Fatalf("iteration %d: sortedClusters()[%d] = %q, want %q (full order: %v)", i, j, key, wantOrder[j], got)
			}
		}
	}
}

func TestEvaluateClusterHealth(t *testing.T) {
	const driveThreshold = 80
	const computeThreshold = 80

	cases := []struct {
		name            string
		mutate          func(*services.WekaStatusResponse)
		drives          []weka.Drive
		wantOK          bool
		wantEmptyReason bool
		wantContains    []string
	}{
		{
			name:            "fully healthy -> allowed",
			mutate:          func(*services.WekaStatusResponse) {},
			wantOK:          true,
			wantEmptyReason: true,
		},
		{
			name: "rebuild not fully protected blocks",
			mutate: func(s *services.WekaStatusResponse) {
				s.Rebuild = services.WekaStatusRebuild{ProtectionState: []services.ProtectionState{{NumFailures: 2, Percent: 3.4}}}
			},
			wantContains: []string{"rebuild not fully protected (2 failures @ 3.4%)"},
		},
		{
			name:         "rebuild moving data blocks even if otherwise protected",
			mutate:       func(s *services.WekaStatusResponse) { s.Rebuild = services.WekaStatusRebuild{MovingData: true} },
			wantContains: []string{"rebuild is moving data"},
		},
		{
			name: "drives INACTIVE block",
			mutate: func(s *services.WekaStatusResponse) {
				s.Drives = services.WekaStatusObjectCounter{Active: 9, Total: 10}
			},
			drives:       []weka.Drive{drv("sn-1", services.DriveStatusInactive)},
			wantContains: []string{"not ACTIVE", "sn-1"},
		},
		{
			name: "drives PHASING_IN block",
			mutate: func(s *services.WekaStatusResponse) {
				s.Drives = services.WekaStatusObjectCounter{Active: 9, Total: 10}
			},
			drives:       []weka.Drive{drv("sn-2", services.DriveStatusPhasingIn)},
			wantContains: []string{"PHASING_IN", "sn-2"},
		},
		{
			name: "drives with unknown/arbitrary status also block (allow-list, not deny-list)",
			mutate: func(s *services.WekaStatusResponse) {
				s.Drives = services.WekaStatusObjectCounter{Active: 9, Total: 10}
			},
			drives:       []weka.Drive{drv("sn-3", "SOME_WEIRD_STATE")},
			wantContains: []string{"SOME_WEIRD_STATE"},
		},
		{
			name: "drive-container threshold: exactly at threshold passes",
			mutate: func(s *services.WekaStatusResponse) {
				s.Containers.Drives = services.WekaStatusObjectCounter{Active: 8, Total: 10}
			},
			wantOK: true,
		},
		{
			name: "drive-container threshold: just below blocks",
			mutate: func(s *services.WekaStatusResponse) {
				s.Containers.Drives = services.WekaStatusObjectCounter{Active: 7, Total: 10}
			},
			wantContains: []string{"drive containers active"},
		},
		{
			name: "drive-container threshold: well below blocks",
			mutate: func(s *services.WekaStatusResponse) {
				s.Containers.Drives = services.WekaStatusObjectCounter{Active: 2, Total: 10}
			},
			wantContains: []string{"20%"},
		},
		{
			name: "compute-container threshold: exactly at threshold passes",
			mutate: func(s *services.WekaStatusResponse) {
				s.Containers.Computes = services.WekaStatusObjectCounter{Active: 8, Total: 10}
			},
			wantOK: true,
		},
		{
			name: "compute-container threshold: just below blocks",
			mutate: func(s *services.WekaStatusResponse) {
				s.Containers.Computes = services.WekaStatusObjectCounter{Active: 7, Total: 10}
			},
			wantContains: []string{"compute containers active"},
		},
		{
			name: "compute-container threshold: well below blocks",
			mutate: func(s *services.WekaStatusResponse) {
				s.Containers.Computes = services.WekaStatusObjectCounter{Active: 2, Total: 10}
			},
			wantContains: []string{"20%"},
		},
		{
			name:   "cluster status OK passes",
			mutate: func(s *services.WekaStatusResponse) { s.Status = "OK" },
			wantOK: true,
		},
		{
			name:   "cluster status REDISTRIBUTING passes",
			mutate: func(s *services.WekaStatusResponse) { s.Status = "REDISTRIBUTING" },
			wantOK: true,
		},
		{
			name:         "cluster status anything else blocks",
			mutate:       func(s *services.WekaStatusResponse) { s.Status = "DEGRADED" },
			wantContains: []string{`weka status is "DEGRADED"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := healthyStatus()
			tc.mutate(status)
			// Expected counts mirror what weka reports, so max() is a no-op here and each case
			// exercises only the field it mutates. The expected-vs-reported divergence itself is
			// covered by TestContainerThresholdReason and the wiring subtest below.
			ok, reason := evaluateClusterHealth(status, tc.drives,
				status.Containers.Drives.Total, status.Containers.Computes.Total,
				driveThreshold, computeThreshold)
			if tc.wantOK {
				if !ok {
					t.Errorf("expected allowed, got blocked: %q", reason)
				}
				if tc.wantEmptyReason && reason != "" {
					t.Errorf("reason = %q, want empty", reason)
				}
				return
			}
			if ok {
				t.Errorf("expected blocked, got allowed")
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(reason, want) {
					t.Errorf("reason = %q, want it to contain %q", reason, want)
				}
			}
		})
	}

	// A cluster that lost 3 of its 10 drive containers reports 7/7 to weka — healthy on its own
	// terms. The expected count is what makes this block.
	t.Run("vanished drive containers block despite a self-consistent weka count", func(t *testing.T) {
		status := healthyStatus()
		status.Containers.Drives = services.WekaStatusObjectCounter{Active: 7, Total: 7}

		ok, _ := evaluateClusterHealth(status, nil,
			status.Containers.Drives.Total, status.Containers.Computes.Total,
			driveThreshold, computeThreshold)
		if !ok {
			t.Fatalf("precondition: weka's own 7/7 count should look healthy, but it blocked")
		}

		ok, reason := evaluateClusterHealth(status, nil,
			10, status.Containers.Computes.Total,
			driveThreshold, computeThreshold)
		if ok {
			t.Errorf("expected blocked once 10 drive containers are expected, got allowed")
		}
		if !strings.Contains(reason, "of drive containers active") {
			t.Errorf("reason = %q, want it to name the drive container threshold", reason)
		}
	})

	// Doesn't fit the mutate-one-field shape above: asserts two joined reasons plus the separator
	// between them, not just a single reason substring.
	t.Run("multiple failing checks are joined with '; '", func(t *testing.T) {
		status := healthyStatus()
		status.Status = "DEGRADED"
		status.Rebuild = services.WekaStatusRebuild{MovingData: true}
		ok, reason := evaluateClusterHealth(status, nil,
			status.Containers.Drives.Total, status.Containers.Computes.Total,
			driveThreshold, computeThreshold)
		if ok {
			t.Errorf("expected blocked")
		}
		if !strings.Contains(reason, "rebuild is moving data") || !strings.Contains(reason, "DEGRADED") {
			t.Errorf("reason = %q, want both failing reasons present", reason)
		}
		if !strings.Contains(reason, "; ") {
			t.Errorf("reason = %q, want multiple reasons joined by '; '", reason)
		}
	})
}

// AllAllowed(nil) == (true, "") means "no dependent clusters to check". That is only safe because the
// gate no longer has a lookup that can silently produce nil verdicts; pin the semantics down.
func TestAllAllowed(t *testing.T) {
	t.Run("nil verdicts -> allowed, empty reason", func(t *testing.T) {
		allowed, reason := AllAllowed(nil)
		if !allowed || reason != "" {
			t.Errorf("AllAllowed(nil) = (%v, %q), want (true, \"\")", allowed, reason)
		}
	})

	t.Run("empty slice -> allowed, empty reason", func(t *testing.T) {
		allowed, reason := AllAllowed([]ClusterVerdict{})
		if !allowed || reason != "" {
			t.Errorf("AllAllowed([]) = (%v, %q), want (true, \"\")", allowed, reason)
		}
	})

	t.Run("all allowed -> allowed, empty reason", func(t *testing.T) {
		verdicts := []ClusterVerdict{
			{Name: "cluster-a", Allowed: true},
			{Name: "cluster-b", Allowed: true},
		}
		allowed, reason := AllAllowed(verdicts)
		if !allowed || reason != "" {
			t.Errorf("AllAllowed(all allowed) = (%v, %q), want (true, \"\")", allowed, reason)
		}
	})

	t.Run("one blocker -> not allowed, its reason verbatim", func(t *testing.T) {
		verdicts := []ClusterVerdict{
			{Name: "cluster-a", Allowed: true},
			{Name: "cluster-b", Allowed: false, Reason: "cluster-b: not protected"},
		}
		allowed, reason := AllAllowed(verdicts)
		if allowed {
			t.Errorf("expected not allowed")
		}
		if reason != "cluster-b: not protected" {
			t.Errorf("reason = %q, want the single blocker's reason verbatim", reason)
		}
	})

	t.Run("multiple blockers -> joined with '; ' preserving input order", func(t *testing.T) {
		verdicts := []ClusterVerdict{
			{Name: "cluster-b", Allowed: false, Reason: "cluster-b: not protected"},
			{Name: "cluster-a", Allowed: true},
			{Name: "cluster-c", Allowed: false, Reason: "cluster-c: rebuild moving data"},
		}
		allowed, reason := AllAllowed(verdicts)
		if allowed {
			t.Errorf("expected not allowed")
		}
		want := "cluster-b: not protected; cluster-c: rebuild moving data"
		if reason != want {
			t.Errorf("reason = %q, want %q (input order preserved)", reason, want)
		}
	})
}

// Regression test for the vacuous post-rotation pass: an empty drives slice must not read as healthy
// when it means "can't tell yet" — a container not yet rejoined, or none observed where source A
// expected them.
func TestNodeDriveVerificationReason(t *testing.T) {
	const node = weka.NodeName("node-1")

	t.Run("skipped > 0 blocks regardless of candidates", func(t *testing.T) {
		got := nodeDriveVerificationReason(0, 2, nil, node)
		want := "cannot verify drives on node node-1 yet: 2 drive container(s) not yet joined to the cluster"
		if got != want {
			t.Errorf("nodeDriveVerificationReason() = %q, want %q", got, want)
		}
	})

	t.Run("skipped > 0 blocks even when candidates and drives are present", func(t *testing.T) {
		drives := []weka.Drive{drv("sn-1", services.DriveStatusActive)}
		got := nodeDriveVerificationReason(1, 1, drives, node)
		want := "cannot verify drives on node node-1 yet: 1 drive container(s) not yet joined to the cluster"
		if got != want {
			t.Errorf("nodeDriveVerificationReason() = %q, want %q", got, want)
		}
	})

	t.Run("candidates > 0 with zero drives and nothing skipped blocks — the vacuous-pass regression", func(t *testing.T) {
		got := nodeDriveVerificationReason(1, 0, nil, node)
		want := "cannot verify drives on node node-1 yet: expected drive containers but observed none"
		if got != want {
			t.Errorf("nodeDriveVerificationReason() = %q, want %q", got, want)
		}
	})

	t.Run("candidates == 0 with zero drives and nothing skipped is a legitimate no-op", func(t *testing.T) {
		if got := nodeDriveVerificationReason(0, 0, nil, node); got != "" {
			t.Errorf("nodeDriveVerificationReason() = %q, want empty (genuinely nothing on this node)", got)
		}
	})

	t.Run("candidates > 0, skipped == 0, one ACTIVE drive passes — the ordinary happy pass", func(t *testing.T) {
		drives := []weka.Drive{drv("sn-1", services.DriveStatusActive)}
		if got := nodeDriveVerificationReason(1, 0, drives, node); got != "" {
			t.Errorf("nodeDriveVerificationReason() = %q, want empty", got)
		}
	})

	t.Run("drives present and all ACTIVE passes regardless of candidates", func(t *testing.T) {
		drives := []weka.Drive{drv("sn-1", services.DriveStatusActive), drv("sn-2", services.DriveStatusActive)}
		if got := nodeDriveVerificationReason(1, 0, drives, node); got != "" {
			t.Errorf("nodeDriveVerificationReason() = %q, want empty", got)
		}
		if got := nodeDriveVerificationReason(0, 0, drives, node); got != "" {
			t.Errorf("nodeDriveVerificationReason() = %q, want empty", got)
		}
	})

	t.Run("drives present but unhealthy delegates to driveHealthReason", func(t *testing.T) {
		drives := []weka.Drive{drv("sn-1", services.DriveStatusInactive)}
		got := nodeDriveVerificationReason(1, 0, drives, node)
		want := "node node-1: 1 drives not ACTIVE (INACTIVE: sn-1)"
		if got != want {
			t.Errorf("nodeDriveVerificationReason() = %q, want %q", got, want)
		}
	})
}
