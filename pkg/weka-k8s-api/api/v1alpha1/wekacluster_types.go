/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/weka/weka-k8s-api/util"
)

type WekaClusterStatusEnum string

const (
	WekaClusterStatusInit         WekaClusterStatusEnum = "Init"
	WekaClusterStatusReady        WekaClusterStatusEnum = "Ready"
	WekaClusterStatusWaitDrives   WekaClusterStatusEnum = "WaitForDrives"
	WekaClusterStatusStartingIO   WekaClusterStatusEnum = "StartingIO"
	WekaClusterStatusPaused       WekaClusterStatusEnum = "Paused"
	WekaClusterStatusGracePeriod  WekaClusterStatusEnum = "GracePeriod"
	WekaClusterStatusDestroying   WekaClusterStatusEnum = "Destroying"
	WekaClusterStatusDeallocating WekaClusterStatusEnum = "Deallocating"
)

type NetworkSelector struct {
	// CIDR subnet (e.g. 192.168.10.0/24) to filter interfaces. Only interfaces with an IP in this subnet are eligible.
	Subnet string `json:"subnet,omitempty"`
	// Minimum number of interfaces required from nodes matching this selector.
	Min int `json:"min,omitempty"`
	// Maximum number of interfaces to select per node matching this selector.
	Max         int      `json:"max,omitempty"`
	DeviceNames []string `json:"deviceNames,omitempty"`
	RdmaOnly    bool     `json:"rdmaOnly,omitempty"`
	DisableRdma bool     `json:"disableRdma,omitempty"`
}

func (n *NetworkSelector) Equal(o *NetworkSelector) bool {
	if n == nil && o == nil {
		return true
	}
	if n == nil || o == nil {
		return false
	}

	return n.Subnet == o.Subnet &&
		n.Min == o.Min &&
		n.Max == o.Max &&
		slices.Equal(n.DeviceNames, o.DeviceNames) &&
		n.RdmaOnly == o.RdmaOnly &&
		n.DisableRdma == o.DisableRdma
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

type Network struct {
	// The name of a single network interface (for example, eth1) to be used by every backend container.
	// This is for clusters that use only one dedicated NIC for the data path.
	// You cannot use this field with ethDevices.
	// If you leave this empty, the system automatically uses the node’s interface associated with the first subnet defined in deviceSubnets.
	EthDevice string `json:"ethDevice,omitempty"`
	// A list of network interface names to be used by backend containers when you have multiple dedicated NICs.
	// The order of interfaces in this list is important, as it maps directly to the ethSlots index (the first interface maps to slot-0, the second to slot-1, and so on).
	// You cannot use this field with ethDevice. Ensure that every interface listed here exists on all nodes that are part of the cluster.
	EthDevices []string `json:"ethDevices,omitempty"`
	// The default gateway IPv4 address for the backend containers’ data-path network.
	// This is only necessary if backend subnets need to communicate with destinations outside of their local network (L2 segment).
	// If you have a flat, non-routed backend network, you can leave this field empty.
	Gateway string `json:"gateway,omitempty"`
	// A setting that enables or disables UDP encapsulation for backend traffic.
	// - false (default): Uses standard raw Ethernet frames. true: Wraps data-path traffic in UDP packets.
	// This is required if your network infrastructure or CNI (Container Network Interface) blocks traffic that isn’t IP-based.
	UdpMode bool `json:"udpMode,omitempty"`
	// A list of backend subnets in CIDR notation (for example, 192.168.10.0/24).
	// The operator assigns IP addresses from these subnets to the backend containers for their data path network
	// +kubebuilder:validation:items:Pattern="^([0-9]{1,3}\\.){3}[0-9]{1,3}\\/[0-9]{1,2}$"
	DeviceSubnets []string `json:"deviceSubnets,omitempty"`
	// Selectors define how backend data-path network interfaces are chosen on each node.
	Selectors []NetworkSelector `json:"selectors,omitempty"`
	// Selectors for management IPs used for cluster API and agent communication.
	ManagementIPsSelectors []NetworkSelector `json:"managementIpsSelectors,omitempty"`
	// BindManagementAll controls whether Weka containers bind to all network interfaces or only to specific management interfaces.
	// When set to false (default), containers will only listen on the management ips interfaces (restrict_listen mode).
	// When set to true, containers will listen on all ips (0.0.0.0) instead of specific IP addresses.
	BindManagementAll bool `json:"bindManagementAll,omitempty"`
	// NvidiaVfSingleIp indicates whether NVIDIA virtual functions (VFs) should be configured to use a single-ip weka mode, where multiple weka processes can share same VF
	// When not set defaults to false, in future releases, when auto-discovery of capabilities will be implemented not set might translate to true on supported setups
	NvidiaVfSingleIp    *bool `json:"nvidiaVfSingleIp,omitempty"`
	AllocateVfPerIoNode *bool `json:"allocateVfPerIoNode,omitempty"`
}

func (n *Network) Equal(o *Network) bool {
	if n == nil && o == nil {
		return true
	}
	if n == nil || o == nil {
		return false
	}

	if n.EthDevice != o.EthDevice {
		return false
	}
	if n.Gateway != o.Gateway {
		return false
	}
	if n.UdpMode != o.UdpMode {
		return false
	}
	if n.BindManagementAll != o.BindManagementAll {
		return false
	}
	if !boolPtrEqual(n.NvidiaVfSingleIp, o.NvidiaVfSingleIp) {
		return false
	}
	if !slices.Equal(n.EthDevices, o.EthDevices) {
		return false
	}
	if !slices.Equal(n.DeviceSubnets, o.DeviceSubnets) {
		return false
	}

	// Compare NetworkSelector slices
	if len(n.Selectors) != len(o.Selectors) {
		return false
	}
	for i, v := range n.Selectors {
		if !v.Equal(&o.Selectors[i]) {
			return false
		}
	}

	if len(n.ManagementIPsSelectors) != len(o.ManagementIPsSelectors) {
		return false
	}
	for i, v := range n.ManagementIPsSelectors {
		if !v.Equal(&o.ManagementIPsSelectors[i]) {
			return false
		}
	}

	return true
}

type AdditionalMemory struct {
	// Additional memory in MiB for compute containers (positive or negative offset from auto-calculated baseline).
	Compute int `json:"compute,omitempty"`
	// Additional memory in MiB for drive containers.
	Drive int `json:"drive,omitempty"`
	// Additional memory in MiB for S3 gateway containers.
	S3 int `json:"s3,omitempty"`
	// Additional memory in MiB for NFS protocol containers.
	Nfs int `json:"nfs,omitempty"`
	// Additional memory in MiB for Envoy proxy containers (used by S3 gateway).
	Envoy        int `json:"envoy,omitempty"`
	Smbw         int `json:"smbw,omitempty"`
	DataServices int `json:"dataServices,omitempty"`
}

func (a *AdditionalMemory) GetForMode(mode string) int {
	additionalMemory := 0
	switch mode {
	case WekaContainerModeDrive:
		additionalMemory = a.Drive
	case WekaContainerModeCompute:
		additionalMemory = a.Compute
	case WekaContainerModeS3:
		additionalMemory = a.S3
	case WekaContainerModeNfs:
		additionalMemory = a.Nfs
	case WekaContainerModeDataServices:
		additionalMemory = a.DataServices
	case WekaContainerModeEnvoy:
		additionalMemory = a.Envoy
	case WekaContainerModeSmbw:
		additionalMemory = a.Smbw
	}
	return additionalMemory
}

// +kubebuilder:validation:XValidation:rule="!has(self.driveCapacity) || self.driveCapacity == 0 || !has(self.driveTypesRatio)",message="driveCapacity and driveTypesRatio are mutually exclusive; use driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types"
// +kubebuilder:validation:XValidation:rule="!has(self.driveCapacity) || self.driveCapacity == 0 || !has(self.numDrives) || self.numDrives == 0 || self.numDrives >= self.driveCores",message="numDrives must be >= driveCores when using driveCapacity (TLC-only mode); each drive core requires at least one virtual drive"
// +kubebuilder:validation:XValidation:rule="!has(self.numDrives) || self.numDrives == 0 || !has(self.containerCapacity) || self.containerCapacity == 0",message="numDrives and containerCapacity are mutually exclusive; use numDrives with driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types"
// +kubebuilder:validation:XValidation:rule="!has(self.containerCapacity) || self.containerCapacity > 0",message="containerCapacity must be greater than 0 when specified"
// +kubebuilder:validation:XValidation:rule="!has(self.driveTypesRatio) || self.driveTypesRatio.tlc > 0 || self.driveTypesRatio.qlc > 0",message="at least one of driveTypesRatio.tlc or driveTypesRatio.qlc must be greater than 0"
// +kubebuilder:validation:XValidation:rule="!has(self.driveTypesRatio) || self.driveTypesRatio.tlc > 0",message="driveTypesRatio.tlc must be greater than 0 when driveTypesRatio is specified; TLC-only and mixed TLC/QLC configurations are supported, but QLC-only is not allowed"
// +kubebuilder:validation:XValidation:rule="!has(self.driveCapacity) || self.driveCapacity > 0",message="driveCapacity must be greater than 0 when specified"
type WekaClusterTemplate struct {
	// Number of compute containers per cluster node.
	// +kubebuilder:validation:Minimum=0
	ComputeContainers int `json:"computeContainers,omitempty"`
	// Number of drive containers per cluster node.
	// +kubebuilder:validation:Minimum=0
	DriveContainers int `json:"driveContainers,omitempty"`
	// Number of S3 gateway containers per cluster node.
	// +kubebuilder:validation:Minimum=0
	S3Containers int `json:"s3Containers,omitempty"`
	// Number of cores allocated to each compute container.
	// +kubebuilder:validation:Minimum=0
	ComputeCores int `json:"computeCores,omitempty"`
	// Number of cores allocated to each drive container.
	// +kubebuilder:validation:Minimum=0
	DriveCores int `json:"driveCores,omitempty"`
	// Number of cores allocated to each S3 gateway container.
	// +kubebuilder:validation:Minimum=0
	S3Cores int `json:"s3Cores,omitempty"`
	// Number of virtual or physical drives per drive container. Mutually exclusive with containerCapacity.
	// +kubebuilder:validation:Minimum=0
	NumDrives int `json:"numDrives,omitempty"`
	// +kubebuilder:validation:Minimum=0
	ComputeExtraCores int `json:"computeExtraCores,omitempty"`
	// +kubebuilder:validation:Minimum=0
	DriveExtraCores int `json:"driveExtraCores,omitempty"`
	// Additional non-DPDK cores for S3 gateway containers, used for background tasks.
	// +kubebuilder:validation:Minimum=0
	S3ExtraCores int `json:"s3ExtraCores,omitempty"`
	// Hugepage allocation in MiB for drive containers. 0 means auto-calculated.
	// +kubebuilder:validation:Minimum=0
	DriveHugepages int `json:"driveHugepages,omitempty"`
	// Offset in MiB applied to the auto-calculated hugepage allocation for drive containers.
	// +kubebuilder:validation:Minimum=0
	DriveHugepagesOffset int `json:"driveHugepagesOffset,omitempty"`
	// Hugepage allocation in MiB for compute containers. 0 means auto-calculated.
	// +kubebuilder:validation:Minimum=0
	ComputeHugepages int `json:"computeHugepages,omitempty"`
	// Offset in MiB applied to the auto-calculated hugepage allocation for compute containers.
	// +kubebuilder:validation:Minimum=0
	ComputeHugepagesOffset int `json:"computeHugepagesOffset,omitempty"`
	// Hugepage allocation in MiB for S3 gateway frontend threads.
	// +kubebuilder:validation:Minimum=0
	S3FrontendHugepages int `json:"s3FrontendHugepages,omitempty"`
	// Offset in MiB applied to the auto-calculated hugepage allocation for S3 frontend threads.
	// +kubebuilder:validation:Minimum=0
	S3FrontendHugepagesOffset int `json:"s3FrontendHugepagesOffset,omitempty"`
	// Number of cores allocated to the Envoy proxy process used by the S3 gateway.
	// +kubebuilder:validation:Minimum=0
	EnvoyCores int `json:"envoyCores,omitempty"`
	// Number of NFS protocol containers per cluster node.
	// +kubebuilder:validation:Minimum=0
	NfsContainers int `json:"nfsContainers,omitempty"`
	// Number of cores allocated to each NFS container.
	// +kubebuilder:validation:Minimum=0
	NfsCores int `json:"nfsCores,omitempty"`
	// Additional non-DPDK cores for NFS containers.
	// +kubebuilder:validation:Minimum=0
	NfsExtraCores int `json:"nfsExtraCores,omitempty"`
	// Hugepage allocation in MiB for NFS frontend threads.
	// +kubebuilder:validation:Minimum=0
	NfsFrontendHugepages int `json:"nfsFrontendHugepages,omitempty"`
	// Offset in MiB for NFS frontend hugepage allocation.
	// +kubebuilder:validation:Minimum=0
	NfsFrontendHugepagesOffset int `json:"nfsFrontendHugepagesOffset,omitempty"`
	// number of SMB-W containers (3-8)
	// +kubebuilder:validation:Minimum=0
	SmbwContainers int `json:"smbwContainers,omitempty"`
	// number of SMB-W cores per container
	// +kubebuilder:validation:Minimum=0
	SmbwCores int `json:"smbwCores,omitempty"`
	// number of SMB-W extra cores per container
	// +kubebuilder:validation:Minimum=0
	SmbwExtraCores int `json:"smbwExtraCores,omitempty"`
	// hugepage allocation for SMB-W frontend
	// +kubebuilder:validation:Minimum=0
	SmbwFrontendHugepages int `json:"smbwFrontendHugepages,omitempty"`
	// hugepage offset for SMB-W frontend
	// +kubebuilder:validation:Minimum=0
	SmbwFrontendHugepagesOffset int `json:"smbwFrontendHugepagesOffset,omitempty"`
	// DriveCapacity is the capacity in GiB to allocate per single virtual drive.
	// NumDrives multiplied by DriveCapacity gives the total capacity requested by each drive container.
	// This value determines how much capacity each container receives from shared drives.
	// +kubebuilder:validation:Minimum=0
	DriveCapacity int `json:"driveCapacity,omitempty"`
	// ContainerCapacity specifies the total capacity (in GiB) requested by each container when using shared drives via SSD proxy.
	// This value takes precedence over DriveCapacity when both are set. It allows more flexible capacity allocation.
	// +kubebuilder:validation:Minimum=0
	ContainerCapacity int `json:"containerCapacity,omitempty"`
	// DriveTypesRatio specifies the desired ratio of drive types (TLC vs QLC) when allocating drives for the cluster.
	DriveTypesRatio *DriveTypesRatio `json:"driveTypesRatio,omitempty"`
	// +kubebuilder:validation:Minimum=0
	DataServicesContainers int `json:"dataServicesContainers,omitempty"`
	// +kubebuilder:validation:Minimum=0
	DataServicesCores      int  `json:"dataServicesCores,omitempty"`
	DataServicesExtraCores *int `json:"dataServicesExtraCores,omitempty"`
	// +kubebuilder:validation:Minimum=0
	DataServicesHugepages int `json:"dataServicesHugepages,omitempty"`
	// +kubebuilder:validation:Minimum=0
	DataServicesHugepagesOffset int `json:"dataServicesHugepagesOffset,omitempty"`
	// +kubebuilder:validation:Minimum=0
	DataServicesFeCores *int `json:"dataServicesFeCores,omitempty"`
}

// GetDataServicesFeCores returns the configured DataServicesFeCores value,
// defaulting to 1 when nil.
func (d *WekaClusterTemplate) GetDataServicesFeCores() int {
	if d == nil || d.DataServicesFeCores == nil {
		return 1
	}
	return *d.DataServicesFeCores
}

type DriveTypesRatio struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	Tlc int `json:"tlc"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	Qlc int `json:"qlc"`
}

// GetTlcQlcCapacity splits total capacity into TLC and QLC based on the ratio.
// Remainder goes to QLC to avoid rounding loss.
// Returns all capacity as TLC if ratio is nil or both values are zero.
func GetTlcQlcCapacity(totalCapacity int, ratio *DriveTypesRatio) (tlc, qlc int) {
	if ratio == nil || ratio.Tlc+ratio.Qlc == 0 {
		return totalCapacity, 0 // All TLC by default
	}
	totalParts := ratio.Tlc + ratio.Qlc
	tlc = (totalCapacity * ratio.Tlc) / totalParts
	qlc = totalCapacity - tlc // Remainder goes to QLC to avoid rounding loss
	return
}

type WekaHomeConfig struct {
	// URL of the WekaHome telemetry endpoint. Defaults to the Weka-managed cloud endpoint if empty.
	Endpoint string `json:"endpoint,omitempty"`
	// When true, disables TLS certificate verification for the WekaHome endpoint.
	AllowInsecure bool `json:"allowInsecure,omitempty"`
	// Name of a Kubernetes secret containing a PEM CA certificate for the WekaHome TLS connection.
	CacertSecret string `json:"cacertSecret,omitempty"`
	// When true, performance statistics are sent to WekaHome in addition to connectivity and event data. Defaults to true.
	EnableStats *bool `json:"enableStats,omitempty"`
}

type RoleNodeSelector struct {
	// nodeSelector for compute weka containers
	Compute *map[string]string `json:"compute,omitempty"`
	// nodeSelector for drive weka containers
	Drive *map[string]string `json:"drive,omitempty"`
	// nodeSelector for s3 weka containers
	S3 *map[string]string `json:"s3,omitempty"`
	// nodeSelector for nfs weka containers
	Nfs *map[string]string `json:"nfs,omitempty"`
	// nodeSelector for smbw weka containers
	Smbw *map[string]string `json:"smbw,omitempty"`
	// nodeSelector for data services weka containers
	DataServices *map[string]string `json:"dataServices,omitempty"`
}

type RoleAnnotations struct {
	// annotations for compute weka containers
	Compute *map[string]string `json:"compute,omitempty"`
	// annotations for drive weka containers
	Drive *map[string]string `json:"drive,omitempty"`
	// annotations for s3 weka containers
	S3 *map[string]string `json:"s3,omitempty"`
	// annotations for nfs weka containers
	Nfs *map[string]string `json:"nfs,omitempty"`
	// annotations for smbw weka containers
	Smbw *map[string]string `json:"smbw,omitempty"`
	// annotations for data services weka containers
	DataServices *map[string]string `json:"dataServices,omitempty"`
}

type RoleNetworkSelector struct {
	// network selector for compute weka containers
	Compute *Network `json:"compute,omitempty"`
	// network selector for drive weka containers
	Drive *Network `json:"drive,omitempty"`
	// network selector for s3 weka containers
	S3 *Network `json:"s3,omitempty"`
	// network selector for nfs weka containers
	Nfs *Network `json:"nfs,omitempty"`
	// network selector for smbw weka containers
	Smbw *Network `json:"smbw,omitempty"`
	// network selector for data services weka containers
	DataServices *Network `json:"dataServices,omitempty"`
}

// RoleCoreIds defines CPU core id lists per container role for Manual CPU policy.
// Each slice contains the core IDs (as visible to the node OS) that should be
// pinned to every container of the corresponding role. If a slice is empty or
// omitted, no explicit core pinning will be applied for that role.
// +kubebuilder:validation:Type=object
// +kubebuilder:validation:Optional
type RoleCoreIds struct {
	// +kubebuilder:validation:Optional
	Compute []int `json:"compute,omitempty"`
	// +kubebuilder:validation:Optional
	Drive []int `json:"drive,omitempty"`
	// +kubebuilder:validation:Optional
	S3 []int `json:"s3,omitempty"`
	// +kubebuilder:validation:Optional
	Nfs []int `json:"nfs,omitempty"`
	// +kubebuilder:validation:Optional
	Smbw []int `json:"smbw,omitempty"`
	// +kubebuilder:validation:Optional
	DataServices []int `json:"dataServices,omitempty"`
}

type RoleTopologySpreadConstraints struct {
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Compute *runtime.RawExtension `json:"compute,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Drive *runtime.RawExtension `json:"drive,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	S3 *runtime.RawExtension `json:"s3,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Nfs *runtime.RawExtension `json:"nfs,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Smbw *runtime.RawExtension `json:"smbw,omitempty"`
}

func (c *RoleTopologySpreadConstraints) ForRole(role string) []v1.TopologySpreadConstraint {
	var raw *runtime.RawExtension
	switch role {
	case "compute":
		raw = c.Compute
	case "drive":
		raw = c.Drive
	case "s3":
		raw = c.S3
	case "nfs":
		raw = c.Nfs
	case "smbw":
		raw = c.Smbw
	default:
		return nil
	}

	constraints, _ := unmarshalTopologySpreadConstraints(raw)
	return constraints
}

type RoleAffinity struct {
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Compute *runtime.RawExtension `json:"compute,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Drive *runtime.RawExtension `json:"drive,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	S3 *runtime.RawExtension `json:"s3,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Nfs *runtime.RawExtension `json:"nfs,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Smbw *runtime.RawExtension `json:"smbw,omitempty"`
}

type PodConfiguration struct {
	// controls the distribution of weka containers across the failure domains
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	TopologySpreadConstraints *runtime.RawExtension `json:"topologySpreadConstraints,omitempty"`
	// takes precedence over the `topologySpreadConstraints`
	RoleTopologySpreadConstraints *RoleTopologySpreadConstraints `json:"roleTopologySpreadConstraints,omitempty"`
	// advanced scheduling constraints
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Affinity *runtime.RawExtension `json:"affinity,omitempty"`
	// affinity per container role
	// takes precedence over the `affinity` field
	RoleAffinity *RoleAffinity `json:"roleAffinity,omitempty"`
}

// unmarshalAffinity safely unmarshals RawExtension to v1.Affinity
func unmarshalAffinity(raw *runtime.RawExtension) (*v1.Affinity, error) {
	if raw == nil || raw.Raw == nil {
		return nil, nil
	}

	var affinity v1.Affinity
	if err := json.Unmarshal(raw.Raw, &affinity); err != nil {
		return nil, fmt.Errorf("failed to unmarshal affinity: %w", err)
	}
	return &affinity, nil
}

// unmarshalTopologySpreadConstraints safely unmarshals RawExtension to []v1.TopologySpreadConstraint
func unmarshalTopologySpreadConstraints(raw *runtime.RawExtension) ([]v1.TopologySpreadConstraint, error) {
	if raw == nil || raw.Raw == nil {
		return nil, nil
	}

	var constraints []v1.TopologySpreadConstraint
	if err := json.Unmarshal(raw.Raw, &constraints); err != nil {
		return nil, fmt.Errorf("failed to unmarshal topologySpreadConstraints: %w", err)
	}
	return constraints, nil
}

type StartIoConditions struct {
	// minimum number of drives that should be added to the cluster before starting IO
	MinNumDrives int `json:"minNumDrives,omitempty"`
}

type FailureDomain struct {
	// label used for spreading the weka containers across different failure domains (if set)
	// nodes that have the same value for the label will be considered as a single failure domain
	Label *string `json:"label,omitempty"`
	// skew for the failure domain, if set, the weka containers will be spread with the skew in mind
	// (only applicable if `label` is set)
	Skew *int `json:"skew,omitempty"`
	// If multiple labels are specified, the failure domain will be the combination of the labels.
	// If `compositeLabels` is set, `label` and `skew` will be ignored.
	// When using compositeLabels, weka containers will be spread considering all labels
	// with best effort, but even distribution is not guaranteed
	CompositeLabels []string `json:"compositeLabels,omitempty"`
}

type CsiConfig struct {
	// CIDR subnets to filter which management IPs are advertised to the CSI driver. Leave empty to advertise all.
	EndpointsSubnets []string `json:"endpointsSubnets,omitempty"`
	// CSI driver group name. Scopes CSI resources when multiple Weka clusters coexist in the same namespace.
	CsiGroup string `json:"csiGroup,omitempty"`
	// Advanced CSI driver settings. Should not be changed unless explicitly instructed by Weka support.
	Advanced *AdvancedCsiConfig `json:"advanced,omitempty"`
}

type VaultConfig struct {
	// Vault address, e.g. "https://vault.example.com:8200".
	Address string `json:"address"`

	// Role to authenticate as in Vault.
	Role string `json:"role"`

	// +kubebuilder:default=kubernetes
	// Path under auth/ that the weka uses for login. defaults to "kubernetes"
	AuthPath string `json:"authPath,omitempty"`

	// +kubebuilder:default=transit
	// Transit engine mount path, defaults "transit".
	TransitPath string `json:"transitPath,omitempty"`

	// +kubebuilder:validation:Enum=kubernetes
	// +kubebuilder:default=kubernetes
	// Vault Auth method (only “kubernetes” is supported  on operator side.)
	Method string `json:"method,omitempty"`

	// +kubebuilder:default=weka-key
	// Name of the transit key. defaults to "weka-key"
	KeyName string `json:"keyName,omitempty"`
}

type InternalEncryptionConfig struct {
	Enabled bool `json:"enabled"`
}

type EncryptionConfig struct {
	// Configures a HashiCorp Vault KMS for encryption key management. Recommended for production.
	VaultConfig *VaultConfig `json:"vault,omitempty"`
	// InternalConfig defines internal encryption settings, encryption key stored in weka configuration, for production systems use real KMS, however this mode can be useful to evaluate performance of encrypted filesystems
	InternalConfig *InternalEncryptionConfig `json:"internal,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.interfaces) || self.interfaces.size() <= 1",message="NFS allows only 1 interface per host"
type NfsConfig struct {
	Interfaces []string `json:"interfaces,omitempty"`
	IpRanges   []string `json:"ipRanges,omitempty"`
}

type S3Config struct {
	// No overlap validation, only appended to the cluster create command as-is
	// Useful for settings such as: `--envoy-max-requests 1150 --envoy-max-connections 1300 --envoy-max-pending-requests 1450`
	// Not propagated to already created cluster, and direct weka control should be used for that
	ClusterCreateArgs []string `json:"clusterCreateArgs,omitempty"`
}

type SmbwConfig struct {
	// ClusterName is the SMB-W cluster name, defaults to "default"
	ClusterName string `json:"clusterName,omitempty"`
	// DomainName is the domain name for SMB-W, required for SMB-W cluster creation
	DomainName       string `json:"domainName"`
	DomainJoinSecret string `json:"domainJoinSecret"`
	UserName         string `json:"userName,omitempty"`
	// IpRanges specifies floating IP ranges for SMB-W high availability
	IpRanges []string `json:"ipRanges,omitempty"`

	// Creation-time configuration flags
	// Symlink enables symlink support for SMB-W shares
	Symlink *bool `json:"symlink,omitempty"`
	// DomainNetbiosName is the NetBIOS name for the domain
	DomainNetbiosName string `json:"domainNetbiosName,omitempty"`
	// IdmapBackend specifies the identity mapping backend (e.g., "ad", "rfc2307")
	IdmapBackend string `json:"idmapBackend,omitempty"`
	// DefaultDomainMappingFromId is the start of the UID/GID range for default domain mapping
	DefaultDomainMappingFromId *int `json:"defaultDomainMappingFromId,omitempty"`
	// DefaultDomainMappingToId is the end of the UID/GID range for default domain mapping
	DefaultDomainMappingToId *int `json:"defaultDomainMappingToId,omitempty"`
	// JoinedDomainMappingFromId is the start of the UID/GID range for joined domain mapping
	JoinedDomainMappingFromId *int `json:"joinedDomainMappingFromId,omitempty"`
	// JoinedDomainMappingToId is the end of the UID/GID range for joined domain mapping
	JoinedDomainMappingToId *int `json:"joinedDomainMappingToId,omitempty"`
	// Encryption specifies the encryption level for SMB connections
	// +kubebuilder:validation:Enum=enabled;disabled;desired;required
	Encryption string `json:"encryption,omitempty"`
	// ScaleOutMode specifies the scale-out mode for SMB-W clustering
	// +kubebuilder:validation:Enum=none;full;partial
	ScaleOutMode string `json:"scaleOutMode,omitempty"`
	// SmbConfExtra contains additional smb.conf configuration
	SmbConfExtra string `json:"smbConfExtra,omitempty"`
	// IpPools specifies IP pools for SMB-W service assignment
	IpPools []string `json:"ipPools,omitempty"`
}

// CatalogConfig defines configuration for the data catalog service
type CatalogConfig struct {
	// IndexInterval specifies how often the catalog index is updated (e.g., "1d", "1m")
	// +kubebuilder:default="1d"
	IndexInterval string `json:"indexInterval,omitempty"`
	// RetentionPeriod specifies how long catalog data is retained (e.g., "30d", "10m")
	// +kubebuilder:default="30d"
	RetentionPeriod string `json:"retentionPeriod,omitempty"`
}

// TelemetryConfig defines the telemetry export configuration for the Weka cluster
type TelemetryConfig struct {
	// List of telemetry exports to configure
	Exports []TelemetryExport `json:"exports,omitempty"`
}

// TelemetryExport defines a single telemetry export destination
type TelemetryExport struct {
	// Name is the unique identifier for this export
	Name string `json:"name"`
	// Sources specifies which telemetry sources to export (e.g., "audit")
	Sources []string `json:"sources"`
	// Splunk configuration for Splunk HEC export
	Splunk *SplunkExportConfig `json:"splunk,omitempty"`
	// Future: S3 *S3ExportConfig `json:"s3,omitempty"`
	// Future: Kafka *KafkaExportConfig `json:"kafka,omitempty"`
}

// SplunkExportConfig defines Splunk-specific export configuration
type SplunkExportConfig struct {
	// AuthTokenSecretRef references a secret containing the Splunk HEC authentication token.
	// Format: "secretName.keyName" where secretName is the name of the secret in the same namespace
	// and keyName is the key within the secret's data that contains the token.
	AuthTokenSecretRef string `json:"authTokenSecretRef"`
	// Endpoint is the Splunk HEC endpoint URL (maps to --target in weka CLI)
	Endpoint string `json:"endpoint"`
	// CACertSecretRef optionally references a secret containing a user-provided CA certificate PEM file.
	// Format: "secretName.keyName" where secretName is the name of the secret in the same namespace
	// and keyName is the key within the secret's data that contains the certificate.
	// Maps to --ca-cert in weka CLI. Empty string is treated same as nil (de-configures if was configured).
	// Mutually exclusive with VerifyWithClusterCACert.
	CACertSecretRef *string `json:"caCertSecretRef,omitempty"`
	// AllowUnverifiedCertificate allows accessing without verifying the target certificate.
	// Maps to --allow-unverified-certificate in weka CLI.
	AllowUnverifiedCertificate bool `json:"allowUnverifiedCertificate,omitempty"`
	// VerifyWithClusterCACert uses the Weka cluster's internal CA certificate to verify.
	// Maps to --verify-with-cluster-cacert in weka CLI.
	// Mutually exclusive with CACertSecretRef.
	VerifyWithClusterCACert bool `json:"verifyWithClusterCACert,omitempty"`
}

// WekaClusterSpec defines the desired state of WekaCluster
type WekaClusterSpec struct {
	// A template/strategy of how to build a cluster, right now only "dynamic" supported, explicitly specifying config of a cluster
	// +kubebuilder:default=dynamic
	Template string `json:"template,omitempty"`
	// full container image name in format of quay.io/weka.io/weka-in-container:VERSION
	// +kubebuilder:validation:Pattern=`^.+:\d+\.\d+\.\d+.*$`
	Image string `json:"image"`
	// image pull secret to use for pulling the image
	ImagePullSecret string `json:"imagePullSecret,omitempty"`
	// endpoint for distribution service, global https://drivers.weka.io or in-k8s-cluster "https://weka-drivers-dist.namespace.svc.cluster.local:60001"
	DriversDistService string `json:"driversDistService,omitempty"`
	// node selector for the weka containers
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// node selector for the weka containers per role, overrides global nodeSelector
	RoleNodeSelector RoleNodeSelector `json:"roleNodeSelector,omitempty"`
	// annotations for the weka containers per role
	RoleAnnotations RoleAnnotations `json:"roleAnnotations,omitempty"`
	// network selector for the weka containers per role, overrides global network
	RoleNetworkSelector RoleNetworkSelector `json:"roleNetworkSelector,omitempty"`
	// failure domain configuration for weka containers
	FailureDomain *FailureDomain `json:"failureDomain,omitempty"`
	// advanced pod affinities configuration
	PodConfig *PodConfiguration `json:"podConfig,omitempty"`
	// cpu policy to use for scheduling cores for weka, unless instructed by weka team, keep default of auto
	// manual and shared are same, with shared being deprecated
	// when manual is used - no exclusive cores will be allocaated on k8s/cgroup level, assuming good alignment of cores usage across different applications, like weka and slurm
	// there is no need to specify siblings in this list, but on the side of other applications like slurm, both weka core and its siblings should be excluded from used cpu set
	//+kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy CpuPolicy `json:"cpuPolicy,omitempty"`
	// traces capacities configuration for weka containers
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	// simplified tolerations, checked only by key existence, expanding to NoExecute|NoSchedule tolerations
	Tolerations []string `json:"tolerations,omitempty"`
	// tolerations in standard k8s format
	RawTolerations []v1.Toleration `json:"rawTolerations,omitempty"`
	// weka home configuration
	WekaHome *WekaHomeConfig `json:"wekaHome,omitempty"`
	// use ipv6 for weka cluster networking configuration
	Ipv6 bool `json:"ipv6,omitempty"`
	// additional memory to allocate for weka containers
	AdditionalMemory AdditionalMemory `json:"additionalMemory,omitempty"`
	// port allocation for weka containers, if not set, free range will be auto selected. Currently allocated ports can be seen in wekacluster.status.ports
	Ports ClusterPorts `json:"ports,omitempty"`
	// reference to the secret containing the weka system credentials used by operator, used in flow of migration
	OperatorSecretRef string `json:"operatorSecretRef,omitempty"`
	// endpoint of existing weka cluster, containers created for this k8s-driver cluster will join existing weka cluster, used in flow of migration
	ExpandEndpoints []string `json:"expandEndpoints,omitempty"`
	// weka cluster topology configuration
	Dynamic *WekaClusterTemplate `json:"dynamicTemplate,omitempty"`
	// weka cluster network configuration
	Network Network `json:"network,omitempty"`
	// A hot spare is reserved capacity designed to handle data rebuilds while maintaining the system's net capacity, even in the event of failure domains being lost
	// See: https://docs.weka.io/weka-system-overview/ssd-capacity-management#hot-spare
	// +kubebuilder:default=0
	HotSpare int `json:"hotSpare,omitempty"`
	// storage capacity dedicated to system protection (2/4). https://docs.weka.io/weka-system-overview/ssd-capacity-management#protection-level
	RedundancyLevel int `json:"redundancyLevel,omitempty"`
	// stripe width is the number of blocks within a common protection set, ranging from 3 to 16 https://docs.weka.io/weka-system-overview/ssd-capacity-management#stripe-width
	StripeWidth int `json:"stripeWidth,omitempty"`
	// size of raft for leadership, defaults to 5, 5/9 are supported
	LeadershipSize *int `json:"leadershipRaftSize,omitempty"`
	// size of raft for buckets, defaults to 5, 5/9 are supported
	BucketRaftSize *int `json:"bucketRaftSize,omitempty"`
	// conditions that must be met before starting IO
	StartIoConditions *StartIoConditions `json:"startIoConditions,omitempty"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +kubebuilder:default="24h"
	// During this period the cluster will not be destroyed (protection from accidental deletion)
	// Note: due to discrepancies in validation vs parsing, we use a Pattern instead of `Format=duration`. See
	// https://bugzilla.redhat.com/show_bug.cgi?id=2050332
	// https://github.com/kubernetes/apimachinery/issues/131
	// https://github.com/kubernetes/apiextensions-apiserver/issues/56
	GracefulDestroyDuration metav1.Duration `json:"gracefulDestroyDuration,omitempty"`
	// Advanced override settings for cluster operations. Only use when explicitly instructed by Weka support.
	Overrides *WekaClusterSpecOverrides `json:"overrides,omitempty"`
	// Configuration for the Weka CSI Driver integration. Controls how the CSI driver discovers and connects to this cluster.
	CsiConfig CsiConfig `json:"csiConfig,omitempty"`
	// Reference to a PVC shared by all Weka containers. Use to persist container state on nodes lacking local NVMe storage.
	GlobalPVC *PVCConfig `json:"globalPVC,omitempty"`
	// Name of the Kubernetes ServiceAccount for Weka container pods. Operator default is used if empty.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// RoleCoreIds defines a list of CPU core IDs (as seen by the host) that should
	// be assigned to containers of the specific role when CpuPolicy is set to
	// "manual". If the slice for the given role is empty, core ids will not be
	// set for that role, and the manual policy will fail validation on pod start.
	//
	// NOTE: The semantics are the same as for NodeSelector/Annotations structures –
	// a single list per role which will be copied to every container of that role.
	// Users are responsible to provide a set that makes sense for their topology.
	// +kubebuilder:validation:Type=object
	// Example:
	//   roleCoreIds:
	//     compute: [0,1,2,3]
	//     drive:   [4,5,6,7]
	//
	// will result in every compute container getting coreIds [0,1,2,3] and every
	// drive container getting [4,5,6,7].
	RoleCoreIds RoleCoreIds `json:"roleCoreIds,omitempty"`
	// RoleNonDatapathCoreIds defines CPU core IDs (as seen by the host) to pin
	// management/aux (non-IONode) processes to, per container role. Applicable
	// when CpuPolicy is "manual" or "shared".
	// When set, weka pins management processes to these cores instead of deriving them automatically.
	// +kubebuilder:validation:Type=object
	RoleNonDatapathCoreIds RoleCoreIds `json:"roleNonDatapathCoreIds,omitempty"`
	// Encryption configuration for data at rest. Configure a HashiCorp Vault KMS for production use.
	Encryption *EncryptionConfig `json:"encryption,omitempty"`
	NFSConfig  *NfsConfig        `json:"nfs,omitempty"`
	S3Config   *S3Config         `json:"s3,omitempty"`
	SmbwConfig *SmbwConfig       `json:"smbw,omitempty"`
	// Telemetry configuration for exporting audit logs and other telemetry data
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`
	// Catalog configuration for data catalog service
	Catalog *CatalogConfig `json:"catalog,omitempty"`
}

func (c *WekaClusterSpec) GetOverrides() *WekaClusterSpecOverrides {
	if c.Overrides == nil {
		return &WekaClusterSpecOverrides{}
	} else {
		return c.Overrides
	}
}

func (c *WekaClusterSpec) GetStartIoConditions() *StartIoConditions {
	if c.StartIoConditions == nil {
		return &StartIoConditions{}
	} else {
		return c.StartIoConditions
	}
}

type PVCConfig struct {
	// Name of the PersistentVolumeClaim to mount into all Weka containers.
	Name string `json:"name"`
	// Mount path inside the Weka container. Defaults to /opt/k8s-weka when empty.
	Path string `json:"path,omitempty"`
}

// DpdkBaseMemoryMbOverride specifies DPDK base memory overrides (in MiB) per container mode.
// Used for hugepages calculation and resources.json configuration. Default value is 64 MiB per core.
// Only positive values are applied; zero or unset means use default.
type DpdkBaseMemoryMbOverride struct {
	Drive        int `json:"drive,omitempty"`
	Compute      int `json:"compute,omitempty"`
	S3           int `json:"s3,omitempty"`
	Nfs          int `json:"nfs,omitempty"`
	Smbw         int `json:"smbw,omitempty"`
	DataServices int `json:"dataServices,omitempty"`
}

type WekaClusterSpecOverrides struct {
	// When true, permits cluster deletion even when an active S3 cluster exists. Destructive — will erase all S3 data.
	AllowS3ClusterDestroy   bool `json:"allowS3ClusterDestroy,omitempty"`
	AllowSmbwClusterDestroy bool `json:"allowSmbwClusterDestroy,omitempty"`
	// disregard redundancy constraints, useful for testing, should not be used in production as misaligns failure domains
	DisregardRedundancy bool `json:"disregardRedundancy,omitempty"`
	// can be used to specify a build_id for a driver in the distributor service, keep empty for auto detection default
	DriversBuildId *string `json:"driversBuildId,omitempty"`
	// image to be used for loading drivers, do not use unless explicitly instructed by Weka team
	DriversLoaderImage string `json:"driversLoaderImage,omitempty"`
	// force weka to use drives in aio mode and not direct nvme (impacts performance, but might serve as a fallback in case of incompatible device)
	ForceAio bool `json:"forceAio,omitempty"`
	// script to run post cluster create (i.e before starting io)
	PostFormClusterScript string `json:"postFormClusterScript,omitempty"`
	// unsafe operation, skips graceful stop of weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel
	UpgradeForceReplace bool `json:"upgradeForceReplace,omitempty"`
	// unsafe operation, skips graceful stop of drive weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel
	UpgradeForceReplaceDrives bool `json:"upgradeForceReplaceDrives,omitempty"`
	// unsafe operation, should not be used unless instructed explicitly by weka personnel
	UpgradeAllAtOnce bool `json:"upgradeAllAtOnce,omitempty"`
	// Pause upgrade
	UpgradePaused bool `json:"upgradePaused,omitempty"`
	// Prevent from moving into compute phase
	UpgradePausePreCompute bool `json:"upgradePausePreCompute,omitempty"`
	// Timeout duration for deactivating pods that are terminating longer than this duration.
	// When nil (default), the default timeout of 5 minutes is used.
	// When set to 0, deactivation of terminating pods is disabled.
	// Otherwise, the specified duration is used.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +optional
	PodTerminationDeactivationTimeout *metav1.Duration `json:"podTerminationDeactivationTimeout,omitempty"`
	// Pause the cluster - all containers will be stopped forcefully.
	// nil (not set): no propagation, allows direct container-level state manipulation.
	// true: pause all containers.
	// false: actively unpause containers that are in paused state.
	Paused *bool `json:"paused,omitempty"`
	// Cancel deletion of the cluster if it is in graceful destroy period, a disaster recovery mechanism
	CancelDeletion   bool                     `json:"cancelDeletion,omitempty"`
	DpdkBaseMemoryMb DpdkBaseMemoryMbOverride `json:"dpdkBaseMemoryMb,omitempty"`
	// used to override machine identifier node reference for backend containers (drive, compute, etc.)
	MachineIdentifierNodeRef string `json:"machineIdentifierNodeRef,omitempty"`
}

func (c *WekaClusterSpec) GetAdditionalMemory(mode string) int {
	return c.AdditionalMemory.GetForMode(mode)
}

type ClusterPorts struct {
	// We should not be updating Spec, as it's a user interface and we should not break ability to update spec file
	// Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here
	BasePort            int `json:"basePort,omitempty"`
	PortRange           int `json:"portRange,omitempty"`
	LbPort              int `json:"lbPort,omitempty"`
	LbAdminPort         int `json:"lbAdminPort,omitempty"`
	S3Port              int `json:"s3Port,omitempty"`
	ManagementProxyPort int `json:"managementProxyPort,omitempty"`
	DataServicesPort    int `json:"dataServicesPort,omitempty"`
}

// WekaClusterStatus defines the observed state of WekaCluster
type WekaClusterStatus struct {
	Status                   WekaClusterStatusEnum  `json:"status"`
	Conditions               []metav1.Condition     `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	ClusterID                string                 `json:"clusterID,omitempty"`
	TraceId                  string                 `json:"traceId,omitempty"`
	SpanID                   string                 `json:"spanId,omitempty"`
	LastAppliedImage         string                 `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	LastAppliedSpec          string                 `json:"lastAppliedSpec,omitempty"`
	LastAppliedPodConfigHash string                 `json:"lastAppliedPodConfigHash,omitempty"`
	Ports                    ClusterPorts           `json:"ports,omitempty"`
	Stats                    *ClusterMetrics        `json:"stats,omitempty"`
	PrinterColumns           ClusterPrinterColumns  `json:"printer,omitempty"`
	Timestamps               map[string]metav1.Time `json:"timestamps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status of the cluster",priority=0
// +kubebuilder:printcolumn:name="Cluster ID",type="string",JSONPath=".status.clusterID",description="Weka cluster GUID",priority=0
// +kubebuilder:printcolumn:name="CCT(A/C/D)",type="string",JSONPath=".status.printer.computeContainers",description="Number of compute containers: Active/Created/Desired",priority=0
// +kubebuilder:printcolumn:name="DCT(A/C/D)",type="string",JSONPath=".status.printer.driveContainers",description="Number of drive containers: Active/Created/Desired",priority=0
// +kubebuilder:printcolumn:name="DRVS(A/C/D)",type="string",JSONPath=".status.printer.drives",description="Number of Drives: Active/Created/Desired",priority=0
// +kubebuilder:printcolumn:name="IOPS(R/W/M)",type="string",JSONPath=".status.printer.iops",description="IOPS Read/Write/Metadata",priority=1
// +kubebuilder:printcolumn:name="THRPT(R/W)",type="string",JSONPath=".status.printer.throughput",description="Throughput Read/Write",priority=1
// +kubebuilder:printcolumn:name="FS(Capacity)",type="string",JSONPath=".status.printer.filesystemCapacity",description="Filesystem Capacity",priority=1

type WekaCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaClusterSpec   `json:"spec,omitempty"`
	Status WekaClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WekaClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaCluster `json:"items"`
}

func (c *WekaCluster) GetOperatorSecretName() string {
	if c.Spec.OperatorSecretRef != "" {
		return c.Spec.OperatorSecretRef
	}
	return string("weka-operator-" + c.GetUID())
}

func (c *WekaCluster) GetLastGuidPart() string {
	return util.GetLastGuidPart(c.GetUID())
}

func (c *WekaCluster) GetUserClusterUsername() string {
	return "weka" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetClusterClientUsername() string {
	return "wekaclient" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetOperatorClusterUsername() string {
	return "weka-operator-" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetInitialOperatorUsername() string {
	return "admin"
}

func (c *WekaCluster) GetUserSecretName() string {
	name := c.Name
	return "weka-cluster-" + name
}

func (c *WekaCluster) GetClientSecretName() string {
	name := c.Name
	return "weka-client-" + name
}

func GetClientSecretName(clusterName string) string {
	return "weka-client-" + clusterName
}

func GetCsiSecretName(clusterName string) string {
	return "weka-csi-" + clusterName
}

func (c *WekaCluster) GetCsiSecretName() string {
	return GetCsiSecretName(c.Name)
}

func (status *WekaClusterStatus) InitStatus() {
	status.Conditions = []metav1.Condition{}
	status.Status = WekaClusterStatusInit
}

func (w *WekaCluster) ToOwnerObject() *WekaOwnerDetails {
	return &WekaOwnerDetails{
		Image:           w.Spec.Image,
		ImagePullSecret: w.Spec.ImagePullSecret,
		Tolerations:     util.ExpandTolerations([]v1.Toleration{}, w.Spec.Tolerations, w.Spec.RawTolerations),
		Labels:          w.ObjectMeta.GetLabels(),
	}
}

func (c *WekaCluster) GetClusterCsiUsername() string {
	return "wekacsi" + c.GetLastGuidPart()
}

func (c *WekaCluster) IsMarkedForDeletion() bool {
	return !c.GetDeletionTimestamp().IsZero()
}

func (c *WekaCluster) IsTerminating() bool {
	return c.Status.Status == WekaClusterStatusDeallocating || c.Status.Status == WekaClusterStatusDestroying
}

func (c *WekaCluster) IsReady() bool {
	return c.Status.Status == WekaClusterStatusReady
}

func (c *WekaCluster) IsExpand() bool {
	return len(c.Spec.ExpandEndpoints) != 0
}

func (c *WekaCluster) GetGracefulDestroyDuration() time.Duration {
	return c.Spec.GracefulDestroyDuration.Duration
}

// Use role-specific node selector if set, otherwise use cluster node selector
func (c *WekaCluster) GetNodeSelectorForRole(role string) map[string]string {
	var roleNodeSelector *map[string]string

	switch role {
	case "compute":
		roleNodeSelector = c.Spec.RoleNodeSelector.Compute
	case "drive":
		roleNodeSelector = c.Spec.RoleNodeSelector.Drive
	case "s3":
		roleNodeSelector = c.Spec.RoleNodeSelector.S3
	case "nfs":
		roleNodeSelector = c.Spec.RoleNodeSelector.Nfs
	case "smbw":
		roleNodeSelector = c.Spec.RoleNodeSelector.Smbw
	case "data-services":
		roleNodeSelector = c.Spec.RoleNodeSelector.DataServices
	}

	if roleNodeSelector != nil {
		return *roleNodeSelector
	} else {
		return c.Spec.NodeSelector
	}
}

// Use role-specific annotations if set, otherwise use cluster annotations
func (c *WekaCluster) GetAnnotationsForRole(role string) map[string]string {
	var roleAnnotations *map[string]string

	switch role {
	case "compute":
		roleAnnotations = c.Spec.RoleAnnotations.Compute
	case "drive":
		roleAnnotations = c.Spec.RoleAnnotations.Drive
	case "s3":
		roleAnnotations = c.Spec.RoleAnnotations.S3
	case "nfs":
		roleAnnotations = c.Spec.RoleAnnotations.Nfs
	case "smbw":
		roleAnnotations = c.Spec.RoleAnnotations.Smbw
	case "data-services":
		roleAnnotations = c.Spec.RoleAnnotations.DataServices
	}

	if roleAnnotations != nil {
		return *roleAnnotations
	} else {
		return c.ObjectMeta.GetAnnotations()
	}
}

// Use role-specific network selector if set, otherwise use cluster network selector
func (c *WekaCluster) GetNetworkForRole(role string) Network {
	var roleNetworkSelector *Network

	switch role {
	case "compute":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Compute
	case "drive":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Drive
	case "s3":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.S3
	case "nfs":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Nfs
	case "smbw":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Smbw
	case "data-services":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.DataServices
	}

	if roleNetworkSelector != nil {
		return *roleNetworkSelector
	} else {
		return c.Spec.Network
	}
}

// GetNonDatapathCoreIdsForRole returns the non-IONode CPU core IDs for the specified role.
func (c *WekaCluster) GetNonDatapathCoreIdsForRole(role string) []int {
	switch role {
	case "compute":
		return c.Spec.RoleNonDatapathCoreIds.Compute
	case "drive":
		return c.Spec.RoleNonDatapathCoreIds.Drive
	case "s3":
		return c.Spec.RoleNonDatapathCoreIds.S3
	case "nfs":
		return c.Spec.RoleNonDatapathCoreIds.Nfs
	case "smbw":
		return c.Spec.RoleNonDatapathCoreIds.Smbw
	case "data-services":
		return c.Spec.RoleNonDatapathCoreIds.DataServices
	default:
		return nil
	}
}

// Return the CPU core IDs for the specified role.
func (c *WekaCluster) GetCoreIdsForRole(role string) []int {
	switch role {
	case "compute":
		return c.Spec.RoleCoreIds.Compute
	case "drive":
		return c.Spec.RoleCoreIds.Drive
	case "s3":
		return c.Spec.RoleCoreIds.S3
	case "nfs":
		return c.Spec.RoleCoreIds.Nfs
	case "smbw":
		return c.Spec.RoleCoreIds.Smbw
	case "data-services":
		return c.Spec.RoleCoreIds.DataServices
	default:
		return nil
	}
}

// Use role-specific affinity if set, otherwise use cluster affinity from PodConfig.
// Returns nil if unmarshaling fails.
func (c *WekaCluster) GetAffinityForRole(role string) *v1.Affinity {
	if c.Spec.PodConfig == nil {
		return nil
	}

	// Try role-specific affinity first
	if c.Spec.PodConfig.RoleAffinity != nil {
		var roleRaw *runtime.RawExtension

		switch role {
		case "compute":
			roleRaw = c.Spec.PodConfig.RoleAffinity.Compute
		case "drive":
			roleRaw = c.Spec.PodConfig.RoleAffinity.Drive
		case "s3":
			roleRaw = c.Spec.PodConfig.RoleAffinity.S3
		case "nfs":
			roleRaw = c.Spec.PodConfig.RoleAffinity.Nfs
		case "smbw":
			roleRaw = c.Spec.PodConfig.RoleAffinity.Smbw
		}

		if roleRaw != nil {
			affinity, err := unmarshalAffinity(roleRaw)
			if err != nil {
				// Log error but don't crash - return nil to skip affinity
				return nil
			}
			if affinity != nil {
				return affinity
			}
		}
	}

	// Fall back to global affinity
	affinity, err := unmarshalAffinity(c.Spec.PodConfig.Affinity)
	if err != nil {
		return nil
	}
	return affinity
}

// Use role-specific topology spread constraints if set, otherwise use cluster topology spread constraints from PodConfig.
// Returns nil if unmarshaling fails.
func (c *WekaCluster) GetTopologySpreadConstraintsForRole(role string) []v1.TopologySpreadConstraint {
	if c.Spec.PodConfig == nil {
		return nil
	}

	// Try role-specific constraints first
	if c.Spec.PodConfig.RoleTopologySpreadConstraints != nil {
		// Use the ForRole() method which now handles RawExtension internally
		constraints := c.Spec.PodConfig.RoleTopologySpreadConstraints.ForRole(role)
		if constraints != nil {
			return constraints
		}
	}

	// Fall back to global constraints
	constraints, err := unmarshalTopologySpreadConstraints(c.Spec.PodConfig.TopologySpreadConstraints)
	if err != nil {
		return nil
	}
	return constraints
}

func init() {
	SchemeBuilder.Register(&WekaCluster{}, &WekaClusterList{})
}
