//go:build darwin

package drive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const volumesPath = "/Volumes"

// findDriveByLabelImpl searches for a drive with the specified volume label on macOS
func findDriveByLabelImpl(label string) (*DriveInfo, error) {
	drives, err := listAllDrivesImpl()
	if err != nil {
		return nil, err
	}

	labelLower := strings.ToLower(label)
	for _, drive := range drives {
		if strings.ToLower(drive.VolumeLabel) == labelLower {
			return &drive, nil
		}
	}

	return nil, fmt.Errorf("drive with label '%s' not found", label)
}

// listAllDrivesImpl returns all available drives on macOS
func listAllDrivesImpl() ([]DriveInfo, error) {
	entries, err := os.ReadDir(volumesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", volumesPath, err)
	}

	var drives []DriveInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		volumeName := entry.Name()
		volumePath := filepath.Join(volumesPath, volumeName)

		// Verify the volume is accessible
		if _, err := os.Stat(volumePath); err != nil {
			continue
		}

		drives = append(drives, DriveInfo{
			Path:        volumePath,
			VolumeLabel: volumeName,
			Letter:      "", // Not applicable on macOS
		})
	}

	return drives, nil
}