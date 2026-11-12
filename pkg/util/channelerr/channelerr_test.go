package channelerr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/exec"
)

func TestIs(t *testing.T) {
	gr := schema.GroupResource{Resource: "pods"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped", Wrap(errors.New("stream broke")), true},
		{"wrapped twice", fmt.Errorf("failed to run: %w - x", Wrap(errors.New("boom"))), true},
		{"internal error", apierrors.NewInternalError(errors.New("etcd")), true},
		{"service unavailable", apierrors.NewServiceUnavailable("down"), true},
		{"server timeout", apierrors.NewServerTimeout(gr, "exec", 1), true},
		{"too many requests", apierrors.NewTooManyRequests("slow down", 1), true},
		{"503 by code", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 503}}, true},
		{"net timeout", &net.DNSError{IsTimeout: true}, true},
		{"net error without timeout", &net.DNSError{}, false},
		{"exit error", fmt.Errorf("command failed: %w", exec.CodeExitError{Err: errors.New("exit 1"), Code: 1}), false},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), false},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), false},
		{"wrapped deadline exceeded", Wrap(context.DeadlineExceeded), true},
		{"not found", apierrors.NewNotFound(gr, "p"), false},
		{"forbidden", apierrors.NewForbidden(gr, "p", errors.New("no")), false},
		{"plain EOF", fmt.Errorf("decode: %w", io.EOF), false},
		{"plain string", errors.New("unable to upgrade connection: container not found"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Is(tt.err); got != tt.want {
				t.Fatalf("Is(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
