package errors

import "fmt"

type ArgumentError struct {
	ArgName string
	Message string
}

func (e ArgumentError) Error() string {
	return fmt.Sprintf("argument error: %s: %s", e.ArgName, e.Message)
}

type WrappedError struct {
	Err error
}

func (e WrappedError) Error() string {
	return fmt.Sprintf("wrapped error: %v", e.Err)
}

func (e WrappedError) Unwrap() error {
	return e.Err
}
