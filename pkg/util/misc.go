package util

func GetMinValue(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func GetNonZeroOrDefault(val int, defaultVal int) int {
	if val == 0 {
		return defaultVal
	}
	return val
}

// StringPtrOrDefault returns the default value if the pointer is nil, otherwise returns the dereferenced value
func StringPtrOrDefault(ptr *string, defaultValue string) string {
	if ptr == nil {
		return defaultValue
	}
	return *ptr
}
