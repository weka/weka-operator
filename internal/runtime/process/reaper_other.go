//go:build !linux

package process

import "context"

// reaper is a no-op on non-Linux platforms: orphan reaping via /proc is Linux-specific.
type reaper struct{}

func newReaper(m *Manager) *reaper { return &reaper{} }

func (r *reaper) run(ctx context.Context) {}

func (r *reaper) stopAndJoin() {}
