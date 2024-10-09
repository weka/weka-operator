package util

func MergeLabels(original, additional map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range original {
		result[k] = v
	}
	for k, v := range additional {
		result[k] = v
	}
	return result
}
