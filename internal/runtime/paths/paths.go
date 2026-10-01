// Package paths holds the filesystem roots the runtime operates under.
package paths

// Roots is the set of filesystem roots the runtime reads and writes.
type Roots struct {
	OptWeka    string // Weka install root
	K8sRuntime string // k8s-runtime state dir under OptWeka
	HostBinds  string // host bind-mount root
	Proc       string // /proc, mountable for host visibility
	Sys        string // /sys, mountable for host visibility
	Tmp        string // scratch dir
	UsrBin     string // /usr/bin, where ssdproxy links weka-sign-drive
}

// Default returns the standard container-root paths.
func Default() Roots {
	return Roots{
		OptWeka:    "/opt/weka",
		K8sRuntime: "/opt/weka/k8s-runtime",
		HostBinds:  "/host-binds",
		Proc:       "/proc",
		Sys:        "/sys",
		Tmp:        "/tmp",
		UsrBin:     "/usr/bin",
	}
}
