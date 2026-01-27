package drive

// DriveInfo contains information about a detected drive
type DriveInfo struct {
	Path        string
	VolumeLabel string
	Letter      string // Windows only (e.g., "E:")
}

// FindDriveByLabel searches for a drive with the specified volume label
// Implementation is in platform-specific files (drive_windows.go, drive_darwin.go)
func FindDriveByLabel(label string) (*DriveInfo, error) {
	return findDriveByLabelImpl(label)
}

// ListAllDrives returns all available drives on the system
// Implementation is in platform-specific files (drive_windows.go, drive_darwin.go)
func ListAllDrives() ([]DriveInfo, error) {
	return listAllDrivesImpl()
}