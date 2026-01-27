//go:build windows

package drive

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	getLogicalDriveStrings = kernel32.NewProc("GetLogicalDriveStringsW")
	getVolumeInformation   = kernel32.NewProc("GetVolumeInformationW")
	getDriveType           = kernel32.NewProc("GetDriveTypeW")
)

const (
	DRIVE_REMOVABLE = 2
	DRIVE_FIXED     = 3
	DRIVE_REMOTE    = 4
	DRIVE_CDROM     = 5
)

// findDriveByLabelImpl searches for a drive with the specified volume label on Windows
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

// listAllDrivesImpl returns all available drives on Windows
func listAllDrivesImpl() ([]DriveInfo, error) {
	// Get the buffer size needed
	bufferSize := uint32(256)
	buffer := make([]uint16, bufferSize)

	ret, _, err := getLogicalDriveStrings.Call(
		uintptr(bufferSize),
		uintptr(unsafe.Pointer(&buffer[0])),
	)

	if ret == 0 {
		return nil, fmt.Errorf("GetLogicalDriveStrings failed: %v", err)
	}

	var drives []DriveInfo

	// Parse the buffer - it contains null-terminated strings
	for i := 0; i < int(ret); {
		// Find the end of the current string
		j := i
		for j < int(ret) && buffer[j] != 0 {
			j++
		}

		if j > i {
			drivePath := syscall.UTF16ToString(buffer[i:j])
			
			// Get volume information
			volumeLabel := getVolumeLabel(drivePath)
			
			// Extract drive letter (e.g., "C:" from "C:\")
			driveLetter := ""
			if len(drivePath) >= 2 {
				driveLetter = drivePath[:2]
			}

			drives = append(drives, DriveInfo{
				Path:        drivePath,
				VolumeLabel: volumeLabel,
				Letter:      driveLetter,
			})
		}

		i = j + 1
	}

	return drives, nil
}

// getVolumeLabel retrieves the volume label for a given drive path
func getVolumeLabel(drivePath string) string {
	volumeNameBuffer := make([]uint16, 256)
	fileSystemNameBuffer := make([]uint16, 256)
	var serialNumber uint32
	var maxComponentLength uint32
	var fileSystemFlags uint32

	drivePathPtr, err := syscall.UTF16PtrFromString(drivePath)
	if err != nil {
		return ""
	}

	ret, _, _ := getVolumeInformation.Call(
		uintptr(unsafe.Pointer(drivePathPtr)),
		uintptr(unsafe.Pointer(&volumeNameBuffer[0])),
		uintptr(len(volumeNameBuffer)),
		uintptr(unsafe.Pointer(&serialNumber)),
		uintptr(unsafe.Pointer(&maxComponentLength)),
		uintptr(unsafe.Pointer(&fileSystemFlags)),
		uintptr(unsafe.Pointer(&fileSystemNameBuffer[0])),
		uintptr(len(fileSystemNameBuffer)),
	)

	if ret == 0 {
		return ""
	}

	return syscall.UTF16ToString(volumeNameBuffer)
}

// GetDriveType returns the type of the specified drive
func GetDriveType(drivePath string) (uint32, error) {
	drivePathPtr, err := syscall.UTF16PtrFromString(drivePath)
	if err != nil {
		return 0, err
	}

	ret, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(drivePathPtr)))
	return uint32(ret), nil
}