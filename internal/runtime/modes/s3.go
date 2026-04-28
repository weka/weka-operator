package modes

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/runtime/config"
)

func init() {
	register("s3", runS3)
}

func runS3(ctx context.Context, cfg *config.Config) error {
	return fmt.Errorf("mode %q not yet implemented", cfg.Mode)
}
