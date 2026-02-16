package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ProcessedFile represents a file that has been processed (kept for backward compatibility)
type ProcessedFile struct {
	Filename    string    `json:"filename"`
	ProcessedAt time.Time `json:"processed_at"`
	ProfileUsed string    `json:"profile_used,omitempty"`
}

// LegacyState represents the old state format (for migration)
type LegacyState struct {
	LastProcessedFile      string          `json:"last_processed_file"`
	LastProcessedTimestamp time.Time       `json:"last_processed_timestamp"`
	ProcessedFiles         []ProcessedFile `json:"processed_files"`
}

// State represents the application state that persists between runs
// Uses time-based tracking: files with mod time <= LastProcessedFileTime are considered processed
type State struct {
	// Version of the state file format
	Version int `json:"version"`

	// CardID identifies the card (based on first file seen or card serial if available)
	CardID string `json:"card_id,omitempty"`

	// LastRun timestamp
	LastRun time.Time `json:"last_run"`
	
	// LastProcessedFileTime is the modification time of the newest file processed
	// Files with mod time <= this are considered already processed
	// This is the PRIMARY mechanism for tracking processed files
	LastProcessedFileTime time.Time `json:"last_processed_file_time,omitempty"`

	// ProcessedFiles is deprecated and kept only for backward compatibility during migration
	// New code should not rely on this - use LastProcessedFileTime instead
	ProcessedFiles map[string]ProcessedFile `json:"processed_files,omitempty"`

	statePath string
}

// DefaultStatePath returns the default path for the state file
func DefaultStatePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %v", err)
	}
	return filepath.Join(homeDir, ".camera-to-immich", "state.json"), nil
}

// Load loads the state from the specified path
func Load(statePath string) (*State, error) {
	state := &State{
		statePath: statePath,
		Version:   3, // Version 3: time-based tracking only
	}

	// Ensure the directory exists
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %v", err)
	}

	data, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		// No state file yet, return empty state
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %v", err)
	}

	// Try to parse as new format first
	if err := json.Unmarshal(data, state); err != nil {
		// Try legacy format
		var legacy LegacyState
		if legacyErr := json.Unmarshal(data, &legacy); legacyErr == nil {
			// Successfully parsed as legacy format, migrate it
			state.LastRun = legacy.LastProcessedTimestamp
			state.Version = 3
			// Note: Legacy format doesn't have LastProcessedFileTime, it will be derived on first run
			state.statePath = statePath
			if saveErr := state.Save(); saveErr != nil {
				// Non-fatal: just log that we couldn't save
				fmt.Printf("Warning: could not save migrated state: %v\n", saveErr)
			}
			return state, nil
		}
		return nil, fmt.Errorf("failed to parse state file: %v", err)
	}

	// Clear deprecated ProcessedFiles on load (migration from v2 to v3)
	if state.Version < 3 && len(state.ProcessedFiles) > 0 {
		state.ProcessedFiles = nil
		state.Version = 3
	}

	state.statePath = statePath
	return state, nil
}

// Save saves the current state to disk
func (s *State) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %v", err)
	}

	if err := os.WriteFile(s.statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %v", err)
	}

	return nil
}

// IsProcessed checks if a file should be considered processed based on its modification time
// Files with mod time <= LastProcessedFileTime are considered already processed
func (s *State) IsProcessed(fileModTime time.Time) bool {
	if s.LastProcessedFileTime.IsZero() {
		return false
	}
	return !fileModTime.After(s.LastProcessedFileTime)
}

// IsProcessedByTime is an alias for IsProcessed (for backward compatibility)
func (s *State) IsProcessedByTime(fileModTime time.Time) bool {
	return s.IsProcessed(fileModTime)
}

// MarkProcessed marks a file as processed by updating the high-water mark
// The fileModTime should be the file's modification time on the card
func (s *State) MarkProcessed(fileModTime time.Time) {
	s.LastRun = time.Now()
	if fileModTime.After(s.LastProcessedFileTime) {
		s.LastProcessedFileTime = fileModTime
	}
}

// MarkProcessedWithTime marks a file as processed and updates the high-water mark
// Kept for backward compatibility - the filename and profileUsed are ignored
func (s *State) MarkProcessedWithTime(filename, profileUsed string, fileModTime time.Time) {
	s.MarkProcessed(fileModTime)
}

// UpdateLastProcessedTime updates the high-water mark for processed files
func (s *State) UpdateLastProcessedTime(fileModTime time.Time) {
	if fileModTime.After(s.LastProcessedFileTime) {
		s.LastProcessedFileTime = fileModTime
	}
}

// GetLastProcessedTime returns the high-water mark timestamp
func (s *State) GetLastProcessedTime() time.Time {
	return s.LastProcessedFileTime
}

// GetProcessedFilesMap returns an empty map (deprecated - use time-based filtering)
func (s *State) GetProcessedFilesMap() map[string]bool {
	return make(map[string]bool)
}

// GetProcessedCount returns 0 (deprecated - no longer tracking individual files)
func (s *State) GetProcessedCount() int {
	return 0
}

// SyncWithCard is deprecated - no longer needed with time-based tracking
func (s *State) SyncWithCard(filesOnCard map[string]bool) int {
	return 0
}

// PruneProcessedFiles clears any remaining entries (migration cleanup)
func (s *State) PruneProcessedFiles() int {
	pruned := len(s.ProcessedFiles)
	s.ProcessedFiles = nil
	return pruned
}

// SetCardID sets an identifier for the current card
func (s *State) SetCardID(id string) {
	s.CardID = id
}

// Clear removes all state including the time-based high-water mark
func (s *State) Clear() int {
	s.ProcessedFiles = nil
	s.CardID = ""
	s.LastRun = time.Time{}
	s.LastProcessedFileTime = time.Time{}
	return 0
}

// ResetToTime sets the high-water mark to a specific time
// Files with mod time > this time will be shown as unprocessed
func (s *State) ResetToTime(t time.Time) {
	s.LastProcessedFileTime = t
	s.ProcessedFiles = nil
}

// Stats returns statistics about the state
type Stats struct {
	LastProcessedTime time.Time
	LastRun           time.Time
	CardID            string
	FileSizeBytes     int64
}

// GetStats returns statistics about the state
func (s *State) GetStats() Stats {
	stats := Stats{
		LastProcessedTime: s.LastProcessedFileTime,
		LastRun:           s.LastRun,
		CardID:            s.CardID,
	}

	if info, err := os.Stat(s.statePath); err == nil {
		stats.FileSizeBytes = info.Size()
	}

	return stats
}
