// Package drivers provides helpers for building and loading Weka kernel drivers.
package drivers

import "strings"

// Driver version constants.
// Mirrors weka_runtime.py:1331-1334.
const (
	IgbUioDriverVersion        = "weka1.0.2"
	MpinUserDriverVersion      = "1.0.1"
	UioPciGenericDriverVersion = "5f49bb7dc1b5d192fb01b442b17ddc0451313ea2"
	DefaultDependencyVersion   = "1.0.0-024f0fdaa33ec66087bc6c5631b85819"
)

// VersionParams holds the per-image-tag driver version parameters.
//
// Tri-state for uio_pci_generic (mirrors Python version_params.get('uio_pci_generic') is not False):
//   - UioPciGenericDisabled = true  → key was explicitly False → skip loading
//   - UioPciGenericDisabled = false, UioPciGeneric = ""  → key absent → load it
//   - UioPciGenericDisabled = false, UioPciGeneric != "" → key is a version string → load that version
//
// Mirrors Python VersionParams dict and DEFAULT_PARAMS at weka_runtime.py:1254-1351.
type VersionParams struct {
	// Wekafs driver version (wekafs key in map). Empty means the default from the image.
	Wekafs string
	// MpinUser driver version (mpin_user key in map). Empty means MPIN_USER_DRIVER_VERSION constant.
	MpinUser string
	// IgbUio driver version (igb_uio key in map). Empty means IGB_UIO_DRIVER_VERSION constant.
	IgbUio string
	// UioPciGeneric holds the version string when uio_pci_generic key is a string, else "".
	UioPciGeneric string
	// UioPciGenericDisabled is true when uio_pci_generic key is explicitly False (skip loading).
	// Mirrors Python: version_params.get('uio_pci_generic') is False at weka_runtime.py:1418.
	UioPciGenericDisabled bool
	// Dependencies version (dependencies key in map).
	Dependencies string
	// WekaDriversHandling is true when the image uses new weka driver subcommands (DEFAULT_PARAMS).
	// False for all explicit map entries (legacy handling).
	// Mirrors Python WEKA_DRIVERS_HANDLING = True if version_params.get("weka_drivers_handling") at weka_runtime.py:1351.
	WekaDriversHandling bool
}

// versionToDriversMapWekafs is a verbatim copy of VERSION_TO_DRIVERS_MAP_WEKAFS.
// Mirrors weka_runtime.py:1254-1325.
// Keys are image tags (the substring after the last ':' in IMAGE_NAME).
var versionToDriversMapWekafs = map[string]VersionParams{
	// 4.3.x-dev entries: uio_pci_generic=False, dependencies="6b519d501ea82063", weka_drivers_handling absent (→ False)
	"4.3.1.29791-9f57657d1fb70e71a3fb914ff7d75eee-dev": {
		Wekafs:                "cc9937c66eb1d0be-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.2.560-842278e2dca9375f84bd3784a4e7515c-dev3": {
		Wekafs:                "1acd22f9ddbda67d-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.2.560-842278e2dca9375f84bd3784a4e7515c-dev4": {
		Wekafs:                "1acd22f9ddbda67d-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.2.560-842278e2dca9375f84bd3784a4e7515c-dev5": {
		Wekafs:                "1acd22f9ddbda67d-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.2.783-f5fe2ec58286d9fa8fc033f920e6c842-dev": {
		Wekafs:                "1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.3.28-k8s-alpha-dev": {
		Wekafs:                "1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.3.28-k8s-alpha-dev2": {
		Wekafs:                "1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.3.28-k8s-alpha-dev3": {
		Wekafs:                "1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.2.783-f5fe2ec58286d9fa8fc033f920e6c842-dev2": {
		Wekafs:                "1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	"4.3.2.783-f5fe2ec58286d9fa8fc033f920e6c842-dev3": {
		Wekafs:                "1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "6b519d501ea82063",
	},
	// 4.2.x entries: uio_pci_generic absent (→ load it), weka_drivers_handling absent (→ False)
	"4.2.7.64-k8so-beta.10": {
		Wekafs: "1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
	},
	"4.2.10.1693-251d3172589e79bd4960da8031a9a693-dev": { // dev 4.2.7-based version
		Wekafs: "1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
	},
	"4.2.10.1290-e552f99e92504c69126da70e1740f6e4-dev": {
		Wekafs: "1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
	},
	"4.2.10-k8so.0": {
		Wekafs: "1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
	},
	"4.2.10.1671-363e1e8fcfb1290e061815445e973310-dev": {
		Wekafs: "1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
	},
	// Plain 4.3.3: uio_pci_generic=False, dependencies="7955984e4bce9d8b", weka_drivers_handling=False
	"4.3.3": {
		Wekafs:                "cbd05f716a3975f7-GW_556972ab1ad2a29b0db5451e9db18748",
		UioPciGenericDisabled: true,
		Dependencies:          "7955984e4bce9d8b",
		WekaDriversHandling:   false,
	},
}

// defaultParams mirrors Python DEFAULT_PARAMS at weka_runtime.py:1335-1338:
//
//	DEFAULT_PARAMS = dict(weka_drivers_handling=True, uio_pci_generic=False)
//
// Unknown image tags fall back to this: new driver handling, uio_pci_generic disabled.
var defaultParams = VersionParams{
	WekaDriversHandling:   true,
	UioPciGenericDisabled: true,
}

// ResolveVersionParams looks up the per-image-tag version parameters.
//
// Algorithm mirrors weka_runtime.py:1339-1348:
//  1. Extract tag = imageName after last ':'.
//  2. Look up tag in VERSION_TO_DRIVERS_MAP_WEKAFS; if absent use DEFAULT_PARAMS.
//  3. If "4.2.7.64-s3multitenancy." appears anywhere in imageName, override wholesale.
func ResolveVersionParams(imageName string) *VersionParams {
	// Extract tag (substring after last ':').
	tag := imageName
	if idx := strings.LastIndex(imageName, ":"); idx >= 0 {
		tag = imageName[idx+1:]
	}

	// Map lookup with DEFAULT_PARAMS fallback.
	// Mirrors: version_params = VERSION_TO_DRIVERS_MAP_WEKAFS.get(IMAGE_NAME.split(":")[-1], DEFAULT_PARAMS)
	params, found := versionToDriversMapWekafs[tag]
	if !found {
		params = defaultParams
	}

	// Wholesale override for 4.2.7.64-s3multitenancy images.
	// Mirrors weka_runtime.py:1342-1348:
	//   if "4.2.7.64-s3multitenancy." in IMAGE_NAME:
	//       version_params = dict(wekafs=..., mpin_user=..., igb_uio=..., uio_pci_generic=...)
	if strings.Contains(imageName, "4.2.7.64-s3multitenancy.") {
		params = VersionParams{
			Wekafs:        "1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
			MpinUser:      "f8c7f8b24611c2e458103da8de26d545",
			IgbUio:        "b64e22645db30b31b52f012cc75e9ea0",
			UioPciGeneric: "1.0.0-929f279ce026ddd2e31e281b93b38f52",
			// WekaDriversHandling absent from override dict → False
			// UioPciGenericDisabled absent → false (string version present → load it)
		}
	}

	return &params
}

// EffectiveUioPciGenericVersion returns the effective uio_pci_generic driver version.
// When UioPciGeneric is set in params use it; otherwise fall back to the global constant.
// Mirrors Python: version_params.get("uio_pci_generic", UIO_PCI_GENERIC_DRIVER_VERSION) used
// in legacy load path at weka_runtime.py:1448.
func (p *VersionParams) EffectiveUioPciGenericVersion() string {
	if p.UioPciGeneric != "" {
		return p.UioPciGeneric
	}
	return UioPciGenericDriverVersion
}

// EffectiveDependencies returns the dependency version, falling back to DefaultDependencyVersion.
// Mirrors Python: version_params.get('dependencies', DEFAULT_DEPENDENCY_VERSION) at weka_runtime.py:2999.
func (p *VersionParams) EffectiveDependencies() string {
	if p.Dependencies != "" {
		return p.Dependencies
	}
	return DefaultDependencyVersion
}

// ShouldSkipUioPciGeneric returns true when uio_pci_generic loading should be skipped.
// Mirrors Python should_skip_uio_pci_generic() at weka_runtime.py:1418:
//
//	return version_params.get('uio_pci_generic') is False or should_skip_uio()
//
// (should_skip_uio() = is_google_cos(); the COS check is the caller's responsibility.)
func (p *VersionParams) ShouldSkipUioPciGeneric() bool {
	return p.UioPciGenericDisabled
}
