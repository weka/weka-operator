package common

import "fmt"

type ArgumentError struct {
	Argument string
	Message  string
}

func (e ArgumentError) Error() string {
	return fmt.Sprintf("Argument %s: %s", e.Argument, e.Message)
}
