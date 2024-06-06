package errors

import (
	"errors"
	"testing"
)

func TestArgumentErrorIs(t *testing.T) {
	err1 := &ArgumentError{ArgName: "arg1", Message: "message1"}
	err2 := &ArgumentError{ArgName: "arg1", Message: "message1"}

	if !errors.Is(err1, err2) {
		t.Errorf("Expected errors to be equal")
	}
}
