package domain

type CompatibilityConfig struct {
	CosEnableHugepagesConfig           bool
	CosHugepageSize                    string
	CosHugepagesCount                  int
	CosDisableDriverSigningEnforcement bool
}
