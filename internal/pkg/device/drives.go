package device

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

//
// Drive Management Implementation
//

type Drive struct {
	// Name is the name of the drive.
	Name string
	// Path is the path to the drive.
	Path string
	UUID string
}

// ListDrives lists the drives available to the device plugin.
// Drives are limited to nvme drives returned by `blkid`.
func ListDrives() ([]Drive, error) {
	pattern := "/sys/class/block/nvme*"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return []Drive{}, errors.Wrapf(err, "failed to list drives under %s", pattern)
	}
	if err != nil {
		return []Drive{}, errors.Wrapf(err, "failed to list drives under %s", pattern)
	}

	if len(matches) == 0 {
		return []Drive{}, errors.Errorf("no drives found under %s", pattern)
	}

	drives := make([]Drive, 0, len(matches))
	// var blkdIdErr error
	for _, match := range matches {
		// Use blkid to get the UUID of the drive.
		//cmd := exec.Command("blkid", match)
		//err := cmd.Run(); err != nil {
		//blkdIdErr = multierror.Append(blkdIdErr, err)
		//continue
		//}

		// drives with the partition file are partitions and should be skipped.
		partitionPath := match + "/partition"
		if _, err := os.Stat(partitionPath); err == nil {
			continue
		}

		drive := Drive{
			Name: filepath.Base(match),
			Path: match,
			UUID: "",
		}
		drives = append(drives, drive)
	}

	return drives, nil
}
