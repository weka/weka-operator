package v1alpha1

import "testing"

// The runtime's MODE_CORES_FLAG keys must match HasWekaCoresMode: BASH_ENV (Go) and the affinity pinning
// (runtime) gate on the two sides of the same set.
func TestHasWekaCoresMode(t *testing.T) {
	withCores := map[string]bool{
		WekaContainerModeDrive: true, WekaContainerModeCompute: true, WekaContainerModeClient: true,
		WekaContainerModeS3: true, WekaContainerModeNfs: true, WekaContainerModeSmbw: true,
		WekaContainerModeDataServices: true,
	}
	for _, mode := range []string{
		WekaContainerModeDist, WekaContainerModeDriversDist, WekaContainerModeDriversLoader,
		WekaContainerModeDriversBuilder, WekaContainerModeCompute, WekaContainerModeDrive,
		WekaContainerModeClient, WekaContainerModeDiscovery, WekaContainerModeS3, WekaContainerModeNfs,
		WekaContainerModeSmbw, WekaContainerModeDataServices, WekaContainerModeEnvoy,
		WekaContainerModeSSDProxy, WekaContainerModeTelemetry, WekaContainerModeAdhocOpWC,
		WekaContainerModeAdhocOp,
	} {
		if got := HasWekaCoresMode(mode); got != withCores[mode] {
			t.Errorf("HasWekaCoresMode(%q) = %v, want %v", mode, got, withCores[mode])
		}
		if got, want := IsBackendMode(mode), withCores[mode] && mode != WekaContainerModeClient; got != want {
			t.Errorf("IsBackendMode(%q) = %v, want %v", mode, got, want)
		}
	}
}
