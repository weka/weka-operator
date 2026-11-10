package podexec

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/exec"
)

type fakeExitError struct{}

func (fakeExitError) Error() string   { return "exit 1" }
func (fakeExitError) String() string  { return "exit 1" }
func (fakeExitError) Exited() bool    { return true }
func (fakeExitError) ExitStatus() int { return 1 }

var _ exec.ExitError = fakeExitError{}

func TestIsChannelError(t *testing.T) {
	gr := schema.GroupResource{Resource: "pods"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"channel error", &ChannelError{Err: errors.New("stream broke")}, true},
		{"wrapped channel error", fmt.Errorf("failed to run: %w - x", &ChannelError{Err: errors.New("boom")}), true},
		{"exit error", fmt.Errorf("command failed: %w", fakeExitError{}), false},
		{"internal error", apierrors.NewInternalError(errors.New("etcd")), true},
		{"service unavailable", apierrors.NewServiceUnavailable("down"), true},
		{"server timeout", apierrors.NewServerTimeout(gr, "exec", 1), true},
		{"timeout", apierrors.NewTimeoutError("slow", 1), true},
		{"too many requests", apierrors.NewTooManyRequests("slow down", 1), true},
		{"net timeout", &net.DNSError{IsTimeout: true}, true},
		{"exec own deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), false},
		{"context canceled", fmt.Errorf("x: %w", context.Canceled), false},
		{"container not running", errors.New("container not running"), false},
		{"upgrade container not found", fmt.Errorf("unable to upgrade connection: container not found"), false},
		{"x509 in url error", &url.Error{Op: "Post", URL: "https://x", Err: x509.UnknownAuthorityError{}}, false},
		{"503 status error", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 503}}, true},
		{"url error econnrefused", &url.Error{Op: "Post", URL: "https://x", Err: syscall.ECONNREFUSED}, true},
		{"stream EOF", fmt.Errorf("stream: %w", io.EOF), true},
		{"not found", apierrors.NewNotFound(gr, "p"), false},
		{"forbidden", apierrors.NewForbidden(gr, "p", errors.New("no")), false},
		{"parse error", errors.New("failed to unmarshal json: invalid character"), false},
		{"status 500 by code", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 500, Reason: metav1.StatusReasonInternalError}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsChannelError(tt.err); got != tt.want {
				t.Fatalf("IsChannelError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
