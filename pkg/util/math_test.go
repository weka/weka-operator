package util

import "testing"

func TestCeilDiv(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{0, 5, 0}, {10, 5, 2}, {11, 5, 3}, {403200, 9600, 42}, {403200, 10000, 41}, {5, 0, 0},
	}
	for _, c := range cases {
		if got := CeilDiv(c.a, c.b); got != c.want {
			t.Errorf("CeilDiv(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRoundDiv(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{0, 5, 0}, {10, 5, 2}, {11, 5, 2}, {13, 5, 3}, {12, 5, 2}, {1440, 240, 6}, {5, 0, 0},
	}
	for _, c := range cases {
		if got := RoundDiv(c.a, c.b); got != c.want {
			t.Errorf("RoundDiv(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
