// Package channelerr marks failures of the path to a target (API server, network, exec transport), as opposed
// to failures reported by the target itself.
package channelerr

import (
	"context"
	"errors"
	"net"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Error marks err as a channel failure. Wrap it only where the error type is still known to be one.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func Wrap(err error) error { return &Error{Err: err} }

// Is reports whether err is a channel failure: a wrapped *Error, an API 5xx or 429, or a network timeout.
// Context expiry is the caller's own deadline, so it is one only when marked by Wrap.
func Is(err error) bool {
	var ce *Error
	if errors.As(err, &ce) {
		return true
	}
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr apierrors.APIStatus
	if errors.As(err, &statusErr) {
		code := statusErr.Status().Code
		return code >= 500 || code == http.StatusTooManyRequests
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
