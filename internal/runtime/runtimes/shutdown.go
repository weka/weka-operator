package runtimes

import (
	"context"
	"errors"

	"github.com/weka/weka-operator/internal/runtime/generation"
	"github.com/weka/weka-operator/internal/runtime/shutdown"
	"golang.org/x/sync/errgroup"
)

// runWekaShutdown runs the shared stop sequence: wait for operator approval if the mode's
// policy requires it, then stop the container if an agent was ever launched for it. A
// takeover recorded at any point force-stops independently of the approval wait, and an
// escalation instruction arriving mid-graceful-stop force-stops the running loop.
// agentLaunched is set before the agent launch attempt; without it no Weka stop is issued.
func runWekaShutdown(ctx context.Context, deps *Deps, mode, name, podID string, agentLaunched bool) error {
	bootID := generation.ReadBootID()
	approval, force := shutdown.StopPolicy(mode)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var g errgroup.Group

	if agentLaunched {
		// Takeover force-stop runs independently of the approval wait (Python takeover_shutdown), once.
		g.Go(func() error {
			select {
			case <-deps.Coord.TakeoverDone():
				return shutdown.ForceStop(wctx, deps.Runner, name)
			case <-wctx.Done():
				return nil
			}
		})
	}

	if approval {
		awaitedForce, err := shutdown.AwaitApproval(ctx, deps.Clock, deps.Paths, podID, bootID)
		if err != nil {
			cancel()
			return errors.Join(err, g.Wait())
		}
		force = force || awaitedForce
	}

	if !agentLaunched {
		cancel()
		return g.Wait()
	}

	if !force {
		g.Go(func() error {
			return shutdown.WatchForce(wctx, shutdown.ForceWatchInput{
				Runner: deps.Runner, Clock: deps.Clock, Paths: deps.Paths, Name: name, PodID: podID, BootID: bootID,
			})
		})
	}

	// TODO: after an asynchronous force stop (takeover or WatchForce) the graceful StopLoop keeps
	// its graceful status policy (agent query failure == still running), as in Python; it ends when
	// the container is observed stopped or the 180s command timeout fires.
	err := shutdown.StopLoop(ctx, deps.Runner, deps.Clock, name, force)
	cancel()
	return errors.Join(err, g.Wait())
}
