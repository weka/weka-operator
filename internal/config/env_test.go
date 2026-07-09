package config_test

import (
	"testing"

	"github.com/weka/weka-operator/internal/config"
)

func TestEffectiveProtection(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.DriveSharingConfig
		specSW   int
		specRL   int
		specHS   int
		wantSW   int
		wantRL   int
		wantHS   int
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
