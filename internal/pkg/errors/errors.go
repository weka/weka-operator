package errors

import (
	"errors"
	"fmt"
	"time"
)

type ArgumentError struct {
	ArgName string
	Message string
}

func (e ArgumentError) Error() string {
	return fmt.Sprintf("argument error: %s: %s", e.ArgName, e.Message)
}

// WrappedError is a base error type that provides a default implementation of the Unwrap method.
type WrappedError struct {
	Err  error
	Span string // Is this name correct?
}

func (e WrappedError) Error() string {
	return fmt.Sprintf("%s > wrapped error: %v", e.Span, e.Err)
}

func (e WrappedError) Unwrap() error {
	return e.Err
}

// NotFoundError is an error type that represents an object not found error.
// This is an alternative to `apierrors.IsNotFound` that is compatible with the
// `errors.As` function.
type NotFoundError struct {
	WrappedError
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("Object not found: %s", e.WrappedError.Error())
}

func IsNotFoundError(err error) bool {
	var e *NotFoundError
	return errors.As(err, &e)
}

// RetryableError is an error type that represents an operation that did not
// complete and should be retried.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %v, retry after: %s", e.Err, e.RetryAfter)
}
