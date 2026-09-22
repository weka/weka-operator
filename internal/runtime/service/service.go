package service

import (
	"context"
	"time"
)

type RuntimeStatus struct {
	Mode      string
	Processes []ProcessStatus
	StartedAt time.Time
}

type ProcessStatus struct {
	Name     string
	Pid      int
	Running  bool
	Restarts int
}

type RuntimeService interface {
	GetStatus(ctx context.Context) (*RuntimeStatus, error)
	Shutdown(ctx context.Context) error
}
