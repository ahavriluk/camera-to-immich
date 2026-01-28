package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the application configuration
type Config struct {
	// Drive settings
	DriveLabel string `json:"drive_label"` // Volume label to search for (default: "OM SYSTEM")

	// File settings
	RawExtensions []string `json:"raw_extensions"` // RAW file extensions to process (e.g., [".ORF", ".CR2", ".NEF", ".ARW"])

	// DNG Conversion settings (for cameras not natively supported by RawTherapee)
	ConvertToDNG         bool   `json:"convert_to_dng"`          // Convert RAW to DNG before RawTherapee processing
	DNGConverterPath     string `json:"dng_converter_path"`      // Path to Adobe DNG Converter (auto-detected if empty)
	DNGOutputDirectory   string `json:"dng_output_directory"`    // Directory for intermediate DNG files (temp dir if empty)
	DNGCompressed        bool   `json:"dng_compressed"`          // Use compressed DNG format (smaller files)
	DNGEmbedOriginal     bool   `json:"dng_embed_original"`      // Embed original raw in DNG (larger files)
	CleanupDNGFiles      bool   `json:"cleanup_dng_files"`       // Delete intermediate DNG files after processing

	// RawTherapee settings
	RawTherapeeExecutable string `json:"rawtherapee_executable"` // Path to rawtherapee-cli
	PP3ProfilePath        string `json:"pp3_profile_path"`       // Path to the PP3 profile
	JPEGQuality           int    `json:"jpeg_quality"`           // JPEG output quality (1-100)
	OutputDirectory       string `json:"output_directory"`       // Directory for processed files

	// Immich settings
	ImmichExecutable string   `json:"immich_executable"` // Path to immich-go
	ImmichServerURL  string   `json:"immich_server_url"` // Immich server URL
	ImmichAPIKey     string   `json:"immich_api_key"`    // Immich API key
	ImmichAlbum      string   `json:"immich_album"`      // Optional album name
	ImmichTags       []string `json:"immich_tags"`       // Additional tags for all uploads

	// Processing options
	ProcessRAWFiles      bool `json:"process_raw_files"`       // Process RAW files with RawTherapee (if false, only upload JPGs)
	UploadCameraJPGs     bool `json:"upload_camera_jpgs"`      // Also upload camera-generated JPGs
	TagWithProfileName   bool `json:"tag_with_profile_name"`   // Tag processed files with profile name
	CleanupAfterUpload   bool `json:"cleanup_after_upload"`    // Delete processed files after successful upload
	DryRun               bool `json:"dry_run"`                 // Don't actually process/upload, just show what would happen
	SkipUpload           bool `json:"skip_upload"`             // Process files but skip uploading to Immich
	Limit                int  `json:"limit"`                   // Limit number of files to process (0 = no limit)
	Workers              int  `json:"workers"`                 // Number of parallel workers for processing (0 = auto based on CPU cores)
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	
	return &Config{
		DriveLabel:          "OM SYSTEM",
		RawExtensions:       []string{".ORF"}, // Olympus RAW format by default
		ConvertToDNG:        false,            // Disabled by default
		CleanupDNGFiles:     true,             // Clean up intermediate DNG files
		JPEGQuality:         92,
		OutputDirectory:     filepath.Join(homeDir, ".camera-to-immich", "output"),
		ProcessRAWFiles:     true,
		UploadCameraJPGs:    true,
		TagWithProfileName:  true,
		CleanupAfterUpload:  true, // Default to cleaning up to save disk space
		DryRun:              false,
	}
}

// DefaultConfigPath returns the default path for the config file
func DefaultConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %v", err)
	}
	return filepath.Join(homeDir, ".camera-to-immich", "config.json"), nil
}

// Load loads configuration from the specified file
func Load(configPath string) (*Config, error) {
	config := DefaultConfig()

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		// Config file doesn't exist, return defaults
		return config, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %v", err)
	}

	return config, nil
}

// Save saves the configuration to the specified file
func (c *Config) Save(configPath string) error {
	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %v", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}

	return nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.DriveLabel == "" {
		return fmt.Errorf("drive_label is required")
	}

	// PP3 profile is only required if RAW processing is enabled
	if c.ProcessRAWFiles {
		if c.PP3ProfilePath == "" {
			return fmt.Errorf("pp3_profile_path is required when process_raw_files is enabled")
		}

		if _, err := os.Stat(c.PP3ProfilePath); os.IsNotExist(err) {
			return fmt.Errorf("PP3 profile not found: %s", c.PP3ProfilePath)
		}
	}

	// Immich settings are only required if upload is enabled
	if !c.SkipUpload {
		if c.ImmichServerURL == "" {
			return fmt.Errorf("immich_server_url is required (use --skip-upload to skip Immich upload)")
		}

		if c.ImmichAPIKey == "" {
			return fmt.Errorf("immich_api_key is required (use --skip-upload to skip Immich upload)")
		}
	}

	if c.JPEGQuality < 1 || c.JPEGQuality > 100 {
		return fmt.Errorf("jpeg_quality must be between 1 and 100")
	}

	return nil
}

// CreateSampleConfig creates a sample configuration file
func CreateSampleConfig(configPath string) error {
	config := DefaultConfig()
	config.RawExtensions = []string{".ORF", ".CR2", ".NEF", ".ARW"} // Example: multiple RAW formats
	config.PP3ProfilePath = "/path/to/your/profile.pp3"
	config.ImmichServerURL = "https://your-immich-server.com"
	config.ImmichAPIKey = "your-api-key-here"
	config.ImmichAlbum = "Camera Uploads"
	config.ImmichTags = []string{"camera", "photography"}
	config.ProcessRAWFiles = true
	// DNG conversion settings (for unsupported cameras like OM-3)
	config.ConvertToDNG = false // Set to true if your camera isn't supported by RawTherapee
	config.CleanupDNGFiles = true

	return config.Save(configPath)
}

// GetRawExtensionsMap returns a map for O(1) extension lookup
func (c *Config) GetRawExtensionsMap() map[string]bool {
	extMap := make(map[string]bool)
	for _, ext := range c.RawExtensions {
		// Normalize to uppercase with leading dot
		normalized := strings.ToUpper(ext)
		if !strings.HasPrefix(normalized, ".") {
			normalized = "." + normalized
		}
		extMap[normalized] = true
	}
	return extMap
}