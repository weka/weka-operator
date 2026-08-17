package config_test

import (
	"testing"

	"github.com/weka/weka-operator/internal/config"
)

func TestEffectiveProtection(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config.DriveSharingConfig
		specSW int
		specRL int
		specHS int
		wantSW int
		wantRL int
		wantHS int
	}{
		{
			name:   "all-zero spec and all-zero defaults yields zero",
			cfg:    config.DriveSharingConfig{},
			wantSW: 0, wantRL: 0, wantHS: 0,
		},
		{
			name:   "all-zero spec falls back to non-zero defaults",
			cfg:    config.DriveSharingConfig{DefaultStripeWidth: 6, DefaultRedundancyLevel: 2, DefaultHotSpare: 1},
			wantSW: 6, wantRL: 2, wantHS: 1,
		},
		{
			name:   "non-zero spec wins over non-zero defaults",
			cfg:    config.DriveSharingConfig{DefaultStripeWidth: 6, DefaultRedundancyLevel: 2, DefaultHotSpare: 1},
			specSW: 4, specRL: 3, specHS: 2,
			wantSW: 4, wantRL: 3, wantHS: 2,
		},
		{
			name:   "mixed: only specSW set, defaults fill the rest",
			cfg:    config.DriveSharingConfig{DefaultStripeWidth: 6, DefaultRedundancyLevel: 2, DefaultHotSpare: 1},
			specSW: 4,
			wantSW: 4, wantRL: 2, wantHS: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw, rl, hs := tt.cfg.EffectiveProtection(tt.specSW, tt.specRL, tt.specHS)
			if sw != tt.wantSW || rl != tt.wantRL || hs != tt.wantHS {
				t.Errorf("EffectiveProtection(%d,%d,%d) = (%d,%d,%d), want (%d,%d,%d)",
					tt.specSW, tt.specRL, tt.specHS,
					sw, rl, hs,
					tt.wantSW, tt.wantRL, tt.wantHS)
			}
		})
	}
}

// TestLoadCapacityEnv_RederivesFormClusterMinimumsAndFullPcpus reproduces the weka-capacity CLI's
// startup order: it scrapes the operator's env and applies it via os.Setenv AFTER this package's
// init() already ran against the CLI's own process environment, then calls only LoadCapacityEnv
// (never ConfigureEnv/init() again). Both the ALLOW_SINGLE_PARITY-lowered form-cluster minimums and
// FullPcpusOnly must therefore be re-derived by LoadCapacityEnv itself, not only by init()/ConfigureEnv.
func TestLoadCapacityEnv_RederivesFormClusterMinimumsAndFullPcpus(t *testing.T) {
	t.Setenv("ALLOW_SINGLE_PARITY", "true")
	t.Setenv("FORM_CLUSTER_MIN_COMPUTE_CONTAINERS", "")
	t.Setenv("FORM_CLUSTER_MIN_DRIVE_CONTAINERS", "")
	t.Setenv("FULL_PCPUS_ONLY", "true")

	config.LoadCapacityEnv()

	if config.Consts.FormClusterMinComputeContainers != 3 {
		t.Errorf("FormClusterMinComputeContainers = %d, want 3 (ALLOW_SINGLE_PARITY-lowered default)", config.Consts.FormClusterMinComputeContainers)
	}
	if config.Consts.FormClusterMinDriveContainers != 3 {
		t.Errorf("FormClusterMinDriveContainers = %d, want 3 (ALLOW_SINGLE_PARITY-lowered default)", config.Consts.FormClusterMinDriveContainers)
	}
	if !config.Config.FullPcpusOnly {
		t.Error("Config.FullPcpusOnly = false, want true: LoadCapacityEnv must read FULL_PCPUS_ONLY for CLI callers")
	}
}
