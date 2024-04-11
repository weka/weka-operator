package util

import (
	"encoding/json"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
)

type WekaDumperConfig struct {
	Enabled              bool  `json:"enabled"`
	EnsureFreeSpaceBytes int64 `json:"ensure_free_space_bytes"`
	FreezePeriod         *struct {
		EndTime   time.Time `json:"end_time"`
		Retention int       `json:"retention"`
		StartTime time.Time `json:"start_time"`
	} `json:"freeze_period,omitempty"`
	RetentionBytes int64  `json:"retention_bytes"`
	RetentionType  string `json:"retention_type"`
	Version        int    `json:"version"`
}

func NewWekaDumperConfig(configuration *v1alpha1.TracesConfiguration) (*WekaDumperConfig, error) {
	if configuration == nil {
		return nil, errors.New("traces configuration is nil")
	}
	// default:
	//{
	//	"enabled":true,
	//	"ensure_free_space_bytes":21474836480,
	//	"freeze_period":
	//		{
	//			"end_time":"0001-01-01T00:00:00+00:00",
	//			"retention":0,
	//			"start_time":"0001-01-01T00:00:00+00:00"
	//		},
	//		"retention_bytes":10737418240,
	//		"retention_type":"BYTES",
	//		"version":1
	//		}

	return &WekaDumperConfig{
		Enabled:              true,
		EnsureFreeSpaceBytes: int64(configuration.EnsureFreeSpace) * 1024 * 1024 * 1024,
		RetentionBytes:       int64(configuration.MaxCapacityPerIoNode) * 1024 * 1024 * 1024,
		RetentionType:        "BYTES",
		Version:              1,
	}, nil
}

func (c *WekaDumperConfig) String() string {
	if c == nil {
		return ""
	}
	data, err := json.Marshal(*c)
	if err != nil {
		return ""
	}
	return string(data)
}
