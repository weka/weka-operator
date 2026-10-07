package wekacontainer

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// No omitempty: a key missing from a stamp means "older operator, not tracked yet" and is skipped,
// so a zero value must still be written or a later 0→N change would never be detected.
type podSpecSnapshot struct {
	NumCores             int                       `json:"numCores"`
	ExtraCores           int                       `json:"extraCores"`
	Hugepages            int                       `json:"hugepages"`
	HugepagesOffset      int                       `json:"hugepagesOffset"`
	NumDrives            int                       `json:"numDrives"`
	AdditionalMemory     int                       `json:"additionalMemory"`
	DpdkBaseMemoryMb     int                       `json:"dpdkBaseMemoryMb"`
	Resources            *weka.PodResourcesSpec    `json:"resources"`
	TracesConfiguration  *weka.TracesConfiguration `json:"tracesConfiguration"`
	PodConfigVersion     string                    `json:"podConfigVersion"`
	PodConfigCodeVersion string                    `json:"podConfigCodeVersion"`
}

const podConfigCodeVersionKey = "podConfigCodeVersion"

func snapshotPodSpec(c *weka.WekaContainer) *podSpecSnapshot {
	traces := c.Spec.TracesConfiguration
	if traces == nil {
		traces = weka.GetDefaultTracesConfiguration()
	}
	return &podSpecSnapshot{
		NumCores:             c.Spec.NumCores,
		ExtraCores:           c.Spec.ExtraCores,
		Hugepages:            c.Spec.Hugepages,
		HugepagesOffset:      c.Spec.HugepagesOffset,
		NumDrives:            c.Spec.NumDrives,
		AdditionalMemory:     c.Spec.AdditionalMemory,
		DpdkBaseMemoryMb:     c.Spec.DpdkBaseMemoryMb,
		Resources:            c.Spec.Resources,
		TracesConfiguration:  traces,
		PodConfigVersion:     config.Config.PodConfigVersion,
		PodConfigCodeVersion: consts.PodConfigCodeVersion,
	}
}

func diffPodSpecSnapshot(stamped string, current *podSpecSnapshot, compareCodeVersion bool) ([]string, error) {
	var old map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stamped), &old); err != nil {
		return nil, fmt.Errorf("invalid %s annotation: %w", consts.PodSpecAnnotation, err)
	}
	curBytes, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	var cur map[string]json.RawMessage
	if err := json.Unmarshal(curBytes, &cur); err != nil {
		return nil, err
	}

	var diff []string
	for key, curRaw := range cur {
		oldRaw, ok := old[key]
		if !ok || (key == podConfigCodeVersionKey && !compareCodeVersion) {
			continue
		}
		var oldVal, curVal any
		if err := json.Unmarshal(oldRaw, &oldVal); err != nil {
			return nil, fmt.Errorf("invalid %s value for %s: %w", consts.PodSpecAnnotation, key, err)
		}
		if err := json.Unmarshal(curRaw, &curVal); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(oldVal, curVal) {
			diff = append(diff, fmt.Sprintf("%s %s→%s", key, oldRaw, curRaw))
		}
	}
	sort.Strings(diff)
	return diff, nil
}
