package v1alpha1

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

type IntMetric struct {
	Value int64       `json:"value,omitempty"`
	Time  metav1.Time `json:"timestamp,omitempty"`
}

func (c *IntMetric) SetValue(v int64, t time.Time) bool {
	if c.Value != v || t.Sub(c.Time.Time) > time.Second*30 {
		c.Value = v
		c.Time = metav1.NewTime(t)
		return true
	}
	return false
}

type FloatMetric struct {
	Value string      `json:"value,omitempty"` // CRD Gen discourages use of float64
	Time  metav1.Time `json:"timestamp,omitempty"`
}

func (c *FloatMetric) SetValue(v float64, t time.Time) bool {
	newVal := fmt.Sprintf("%.2f", v)
	if newVal != c.Value || t.Sub(c.Time.Time) > time.Second*30 {
		c.Value = newVal
		c.Time = metav1.NewTime(t)
		return true
	}
	return false
}

type StringMetric struct {
	Value string      `json:"value,omitempty"`
	Time  metav1.Time `json:"timestamp,omitempty"`
}

func (c *StringMetric) SetValue(v string, t time.Time) bool {
	if c.Value != v || t.Sub(c.Time.Time) > time.Second*30 {
		c.Value = v
		c.Time = metav1.NewTime(t)
		return true
	}
	return false
}

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
	Containers EntityStatefulNum `json:"numContainers,omitempty"`
	Processes  EntityStatefulNum `json:"processes,omitempty"`
	CpuUsage   MinMaxAvgPercent  `json:"cpuUsage,omitempty"`
}

func (c *EntityStatefulNum) String() string {
	return fmt.Sprintf("%d/%d/%d", c.Active.Value, c.Created.Value, c.Desired.Value)
}

type ContainersMetrics struct {
	Drive   ContainerMetrics  `json:"drive,omitempty"`
	Compute ContainerMetrics  `json:"compute,omitempty"`
	S3      *ContainerMetrics `json:"s3,omitempty"`
	Nfs     *ContainerMetrics `json:"nfs,omitempty"`
}

type DriveFailures struct {
	SerialId      string `json:"serialId,omitempty"`
	NodeName      string `json:"nodeName,omitempty"`
	WekaDriveId   string `json:"wekaDriveId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
}

type DriveMetrics struct {
	DriveCounters EntityStatefulNum `json:"driveCounters,omitempty"`
	DriveFailures DriveFailures     `json:"driveFailures,omitempty"`
}

type IoStats struct {
	Throughput StatusThroughput `json:"throughput,omitempty"`
	Iops       StatusIops       `json:"iops,omitempty"`
}

type ClusterMetrics struct {
	Containers ContainersMetrics `json:"containers,omitempty"`
	IoStats    IoStats           `json:"ioStats,omitempty"`
	Drives     DriveMetrics      `json:"drives,omitempty"`
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

type StatusIops struct {
	Read     IntMetric `json:"read"`
	Write    IntMetric `json:"write"`
	Metadata IntMetric `json:"metadata"`
	Total    IntMetric `json:"total"`
}
