package util

// CeilDiv returns ceil(a/b) for positive b; 0 when a<=0 or b<=0.
func CeilDiv(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// RoundDiv returns a/b rounded to the nearest integer for positive b; 0 when a<=0 or b<=0.
func RoundDiv(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a + b/2) / b
}
