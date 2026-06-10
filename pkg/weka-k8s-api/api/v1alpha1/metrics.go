package v1alpha1

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type IntMetric int64

func (in IntMetric) String() string {
	return fmt.Sprintf("%d", in)
}

type FloatMetric string

func (c *FloatMetric) SetValue(v float64) {
	newVal := fmt.Sprintf("%.2f", v)
	*c = FloatMetric(newVal)
}

func NewFloatMetric(v float64) FloatMetric {
	newVal := fmt.Sprintf("%.2f", v)
	return FloatMetric(newVal)
}

func (c *FloatMetric) GetValue() float64 {
	// load string as float64
	rawVal := *c
	value, err := strconv.ParseFloat(string(rawVal), 64)
	if err != nil {
		fmt.Println("Error:", err)
		return 0
	}
	return value
}

type StringMetric string

type EntityStatefulNum struct {
	Active  IntMetric `json:"active,omitempty"`
	Created IntMetric `json:"created,omitempty"`
	Desired IntMetric `json:"desired,omitempty"`
}

type MinMaxAvgPercent struct {
	Min FloatMetric `json:"min,omitempty"`
	Max FloatMetric `json:"max,omitempty"`
	Avg FloatMetric `json:"avg,omitempty"`
}

type ContainerMetrics struct {
	Containers     EntityStatefulNum `json:"numContainers,omitempty"`
	Processes      EntityStatefulNum `json:"processes,omitempty"`
	CpuUtilization FloatMetric       `json:"cpuUtilization,omitempty"`
}

// String formats the counts for a printer column. When Desired is 0 (e.g.
// clusterCapacity mode, where the target count is planner-driven and not known
// in advance) the desired segment is omitted, yielding "active/created".
func (c *EntityStatefulNum) String() string {
	if c.Desired == 0 {
		return fmt.Sprintf("%d/%d", c.Active, c.Created)
	}
	return fmt.Sprintf("%d/%d/%d", c.Active, c.Created, c.Desired)
}

type ContainersMetrics struct {
	Drive   ContainerMetrics  `json:"drive,omitempty"`
	Compute ContainerMetrics  `json:"compute,omitempty"`
	S3      *ContainerMetrics `json:"s3,omitempty"`
	Nfs     *ContainerMetrics `json:"nfs,omitempty"`
}

type DriveFailures struct {
	SerialId    string `json:"serialId,omitempty"`
	WekaDriveId string `json:"wekaDriveId,omitempty"`
}

type DriveMetrics struct {
	DriveCounters EntityStatefulNum `json:"counters,omitempty"`
	DriveFailures []DriveFailures   `json:"failures,omitempty"`
}

type IoStats struct {
	Throughput StatusThroughput `json:"throughput,omitempty"`
	Iops       StatusIops       `json:"iops,omitempty"`
}

type CapacityMetrics struct {
	TotalBytes         IntMetric `json:"totalBytes,omitempty"`
	UnprovisionedBytes IntMetric `json:"unprovisionedBytes,omitempty"`
	UnavailableBytes   IntMetric `json:"unavailableBytes,omitempty"`
	HotSpareBytes      IntMetric `json:"hotSpareBytes,omitempty"`
}

// FilesystemMetrics contains metrics about filesystem usage
type FilesystemMetrics struct {
	// TotalProvisionedCapacity is the sum of total_budget for all filesystems
	TotalProvisionedCapacity IntMetric `json:"totalProvisionedCapacity,omitempty"`

	// TotalUsedCapacity is the sum of used_total for all filesystems
	TotalUsedCapacity IntMetric `json:"totalUsedCapacity,omitempty"`

	// TotalAvailableCapacity is the difference between TotalProvisionedCapacity and TotalUsedCapacity
	TotalAvailableCapacity IntMetric `json:"totalAvailableCapacity,omitempty"`

	// SSD-specific metrics
	TotalProvisionedSSDCapacity IntMetric `json:"totalProvisionedSSDCapacity,omitempty"`
	TotalUsedSSDCapacity        IntMetric `json:"totalUsedSSDCapacity,omitempty"`
	TotalAvailableSSDCapacity   IntMetric `json:"totalAvailableSSDCapacity,omitempty"`

	// Object Store metrics
	HasTieredFilesystems bool      `json:"hasTieredFilesystems,omitempty"`
	TotalObsCapacity     IntMetric `json:"totalObsCapacity,omitempty"`
	ObsBucketCount       IntMetric `json:"obsBucketCount,omitempty"`
	ActiveObsBucketCount IntMetric `json:"activeObsBucketCount,omitempty"`
}

type ClusterMetrics struct {
	Containers    ContainersMetrics      `json:"containers,omitempty"`
	IoStats       IoStats                `json:"ioStats,omitempty"`
	Drives        DriveMetrics           `json:"drives,omitempty"`
	AlertsCount   IntMetric              `json:"alertsCount,omitempty"`
	ClusterStatus StringMetric           `json:"clusterStatus,omitempty"`
	Capacity      CapacityMetrics        `json:"capacity,omitempty"`
	NumFailures   map[string]FloatMetric `json:"numFailures,omitempty"`
	LastUpdate    metav1.Time            `json:"lastUpdate,omitempty"`
	Filesystem    FilesystemMetrics      `json:"filesystem,omitempty"`
}

type ClusterPrinterColumns struct {
	ComputeContainers StringMetric `json:"computeContainers,omitempty"`
	DriveContainers   StringMetric `json:"driveContainers,omitempty"`
	Drives            StringMetric `json:"drives,omitempty"`
	Throughput        StringMetric `json:"throughput,omitempty"`
	Iops              StringMetric `json:"iops,omitempty"`
	// Information about filesystem capacity: Available/Used
	FilesystemCapacity StringMetric `json:"filesystemCapacity,omitempty"`
	// Aggregated raw provisioned drive-sharing capacity per type, e.g. "TLC 100TiB / QLC 200TiB"
	Capacity StringMetric `json:"capacity,omitempty"`
}

type StatusThroughput struct {
	Read  IntMetric `json:"read"`
	Write IntMetric `json:"write"`
}

func (c *StatusThroughput) String() string {
	return fmt.Sprintf("%d/%d", c.Read, c.Write)
}

func (c *StatusThroughput) Total() string {
	return fmt.Sprintf("%d", c.Read+c.Write)
}

type StatusIops struct {
	Read     IntMetric `json:"read"`
	Write    IntMetric `json:"write"`
	Metadata IntMetric `json:"metadata"`
	Total    IntMetric `json:"total"`
}
