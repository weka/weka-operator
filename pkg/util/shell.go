package util

func BoolToShellString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
