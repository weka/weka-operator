package v1alpha1

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strconv"
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

func (c *EntityStatefulNum) String() string {
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

type ClusterMetrics struct {
	Containers ContainersMetrics `json:"containers,omitempty"`
	IoStats    IoStats           `json:"ioStats,omitempty"`
	Drives     DriveMetrics      `json:"drives,omitempty"`
	LastUpdate metav1.Time       `json:"lastUpdate,omitempty"`
}

type ClusterPrinterColumns struct {
	ComputeContainers StringMetric `json:"computeContainers,omitempty"`
	DriveContainers   StringMetric `json:"driveContainers,omitempty"`
	Drives            StringMetric `json:"drives,omitempty"`
	Throughput        StringMetric `json:"throughput,omitempty"`
	Iops              StringMetric `json:"iops,omitempty"`
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
