package resources

import (
	"os"
	"regexp"
	"testing"
)

// Every WekaContainer spec field pod.go reads must be either stamped (a change replaces the pod) or
// excluded with a reason; a new read in neither list silently never rotates.
var podSpecTracked = map[string]bool{
	"NumCores": true, "ExtraCores": true, "Hugepages": true, "HugepagesOffset": true, "NumDrives": true,
	"AdditionalMemory": true, "DpdkBaseMemoryMb": true, "Resources": true, "TracesConfiguration": true,
}

var podSpecExcluded = map[string]string{
	"Image":                     "own flow (image roll)",
	"NodeSelector":              "scheduling; deleteIfNodeSelectorMismatch",
	"Tolerations":               "scheduling; deleteIfTolerationsMismatch",
	"Affinity":                  "scheduling",
	"NoAffinityConstraints":     "scheduling",
	"TopologySpreadConstraints": "scheduling",
	"JoinIps":                   "changes during expansion; must not restart pods",
	"ImagePullSecret":           "pull-time only",
	"DriveCapacity":             "set at creation, no writer after (O6)",
	"ContainerCapacity":         "planner grows it live, no restart (O6)",
	"DriveTypesRatio":           "planner grows it live, no restart (O6)",
	"HugepagesSize":             "set at creation, no writer after (O6)",
	"Mode":                      "immutable identity",
	"WekaContainerName":         "immutable identity",
	// v2 candidates (D2): need a "restart required?" check first
	"ExtraVolumeMounts": "v2", "ExtraVolumes": "v2", "AdditionalSecrets": "v2", "CpuPolicy": "v2",
	"CoreIds": "v2", "NonDatapathCoreIds": "v2", "Numa": "v2", "Network": "v2",
	"DriversDistService": "v2", "DriversBuildId": "v2", "DriversLoaderImage": "v2",
	"GetExtraVolumes": "v2 (reads ExtraVolumes)", "GetOverrides": "v2 (reads Overrides, written by upgrade flow)",
	"DataServicesConfig": "v2", "AutoRemoveTimeout": "v2", "PortRange": "v2", "WekaSecretRef": "v2",
	"HostPID": "v2", "PVC": "v2", "ServiceAccountName": "v2", "ExposePorts": "v2", "Ipv6": "v2",
	"NodeInfoConfigMap": "v2", "Instructions": "own flow (set per operation, not a config change)",
	"NamedHugepages2MiMiB": "derived from Resources (tracked)",
}

func TestPodSpecReadsAreClassified(t *testing.T) {
	src, err := os.ReadFile("pod.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`container\.Spec\.([A-Z][A-Za-z0-9]*)`).FindAllStringSubmatch(string(src), -1) {
		f := m[1]
		if !podSpecTracked[f] && podSpecExcluded[f] == "" {
			t.Errorf("pod.go reads container.Spec.%s: add it to the stamp (wekacontainer/pod_spec_snapshot.go + podSpecTracked) or to podSpecExcluded with a reason", f)
		}
	}
}
