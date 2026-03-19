//go:build linux

package drive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Common Linux mount points for removable media
var linuxVolumePaths = []string{
	"/media",
	"/mnt",
	"/run/media",
}

// findDriveByLabelImpl searches for a drive with the specified volume label on Linux
func findDriveByLabelImpl(label string) (*DriveInfo, error) {
	drives, err := listAllDrivesImpl()
	if err != nil {
		return nil, err
	}

	labelLower := strings.ToLower(label)
	for _, d := range drives {
		if strings.ToLower(d.VolumeLabel) == labelLower {
			return &d, nil
		}
	}

	return nil, fmt.Errorf("drive with label '%s' not found", label)
}

// listAllDrivesImpl lists all mounted volumes on Linux
func listAllDrivesImpl() ([]DriveInfo, error) {
	var drives []DriveInfo

	for _, basePath := range linuxVolumePaths {
		// Check if the base path exists
		info, err := os.Stat(basePath)
		if err != nil || !info.IsDir() {
			continue
		}

		// For /media and /run/media, volumes may be under a username subdirectory
		if basePath == "/media" || basePath == "/run/media" {
			// List user dirs first
			userDirs, err := os.ReadDir(basePath)
			if err != nil {
				continue
			}
			for _, userDir := range userDirs {
				if !userDir.IsDir() {
					continue
				}
				userPath := filepath.Join(basePath, userDir.Name())
				volumeDirs, err := os.ReadDir(userPath)
				if err != nil {
					continue
				}
				for _, vol := range volumeDirs {
					if !vol.IsDir() {
						continue
					}
					drives = append(drives, DriveInfo{
						Path:        filepath.Join(userPath, vol.Name()),
						VolumeLabel: vol.Name(),
					})
				}
			}
		} else {
			// For /mnt, list direct subdirectories
			entries, err := os.ReadDir(basePath)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				drives = append(drives, DriveInfo{
					Path:        filepath.Join(basePath, entry.Name()),
					VolumeLabel: entry.Name(),
				})
			}
		}
	}

	if len(drives) == 0 {
		return nil, fmt.Errorf("no mounted volumes found in %v", linuxVolumePaths)
	}

	return drives, nil
}