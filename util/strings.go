package util

func TruncateString(str string, num int) string {
	runeStr := []rune(str)
	if len(runeStr) > num {
		return string(runeStr[:num])
	}
	return str
}
