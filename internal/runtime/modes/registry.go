package modes

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/runtime/config"
)

type ModeFunc func(ctx context.Context, cfg *config.Config) error

var registry = map[string]ModeFunc{}

func register(name string, fn ModeFunc) {
	registry[name] = fn
}

func Run(ctx context.Context, cfg *config.Config) error {
	fn, ok := registry[cfg.Mode]
	if !ok {
		return fmt.Errorf("unknown mode: %q", cfg.Mode)
	}
	return fn(ctx, cfg)
}
