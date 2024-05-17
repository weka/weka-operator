package errors

import "fmt"

type ArgumentError struct {
	ArgName string
	Message string
}

func (e ArgumentError) Error() string {
	return fmt.Sprintf("argument error: %s: %s", e.ArgName, e.Message)
}
