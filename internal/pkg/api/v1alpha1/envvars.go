package v1alpha1

import "os"

const EnvOCPPullSecret = "WEKA_OCP_PULL_SECRET"
const EnvOCPToolkitImage = "WEKA_OCP_TOOLKIT_IMAGE"
const EnvCOSServiceAccount = "WEKA_COS_SERVICE_ACCOUNT_SECRET"
const EnvCosAllowHugePagesConfig = "WEKA_COS_ALLOW_HUGEPAGE_CONFIG"
const EnvCosHugePagesSize = "WEKA_COS_GLOBAL_HUGEPAGE_SIZE"
const EnvCosHugePagesCount = "WEKA_COS_GLOBAL_HUGEPAGE_COUNT"
const EnvCosAllowSigningDisable = "WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING"

func GetStringEnv(key string, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func GetBoolEnv(key string, defaultValue bool) bool {
	if value, ok := os.LookupEnv(key); ok {
		return value == "true"
	}
	return defaultValue
}
