package validation

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// roleSpec is one container role's sizing fields together with the spec field names used in
// messages. The *Field names are the JSON spec names, not derivable from role — the frontend
// roles spell hugepages as s3FrontendHugepages/nfsFrontendHugepages/smbwFrontendHugepages.
type roleSpec struct {
	role            string
	coresField      string
	hugepagesField  string
	containersField string

	cores      int
	hugepages  int // MiB per container
	containers int

	// countDerived marks a role whose container count the operator supplies when the spec leaves it
	// unset: Drive and Compute get a floor from allocator.GetWekaContainerNumbers, and a cluster acting
	// as a daemonset leaves both unset by definition. For the frontend roles an unset count is literal —
	// the role deploys nothing — so a per-container check on them has nothing to check.
	countDerived bool
}

// rolesForTemplate pairs every container role with the template's values for it, so the six-role
// list exists once instead of per validator. Nil-safe: a nil template (auto-full-drives mode) has
// no per-role values to check, and a panic here would reject every apply through failurePolicy: Fail.
func rolesForTemplate(d *weka.WekaClusterTemplate) []roleSpec {
	if d == nil {
		return nil
	}
	return []roleSpec{
		{weka.WekaContainerModeDrive, "driveCores", "driveHugepages", "driveContainers",
			d.DriveCores, d.DriveHugepages, d.DriveContainers, true},
		{weka.WekaContainerModeCompute, "computeCores", "computeHugepages", "computeContainers",
			d.ComputeCores, d.ComputeHugepages, d.ComputeContainers, true},
		{weka.WekaContainerModeS3, "s3Cores", "s3FrontendHugepages", "s3Containers",
			d.S3Cores, d.S3FrontendHugepages, d.S3Containers, false},
		{weka.WekaContainerModeNfs, "nfsCores", "nfsFrontendHugepages", "nfsContainers",
			d.NfsCores, d.NfsFrontendHugepages, d.NfsContainers, false},
		{weka.WekaContainerModeSmbw, "smbwCores", "smbwFrontendHugepages", "smbwContainers",
			d.SmbwCores, d.SmbwFrontendHugepages, d.SmbwContainers, false},
		{weka.WekaContainerModeDataServices, "dataServicesCores", "dataServicesHugepages", "dataServicesContainers",
			d.DataServicesCores, d.DataServicesHugepages, d.DataServicesContainers, false},
	}
}
