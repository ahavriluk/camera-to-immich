package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ProcessedFile represents a file that has been processed
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
// Only tracks files from the current/last connected card
type State struct {
	// Version of the state file format
	Version int `json:"version"`

	// CardID identifies the card (based on first file seen or card serial if available)
	CardID string `json:"card_id,omitempty"`

	// LastRun timestamp
	LastRun time.Time `json:"last_run"`

	// ProcessedFiles tracks files that have been processed from the current card
	ProcessedFiles map[string]ProcessedFile `json:"processed_files"`

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
		statePath:      statePath,
		ProcessedFiles: make(map[string]ProcessedFile),
		Version:        2,
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
			state.ProcessedFiles = make(map[string]ProcessedFile)
			for _, pf := range legacy.ProcessedFiles {
				state.ProcessedFiles[pf.Filename] = pf
			}
			state.LastRun = legacy.LastProcessedTimestamp
			state.Version = 2
			// Save in new format
			state.statePath = statePath
			if saveErr := state.Save(); saveErr != nil {
				// Non-fatal: just log that we couldn't save
				fmt.Printf("Warning: could not save migrated state: %v\n", saveErr)
			}
			return state, nil
		}
		return nil, fmt.Errorf("failed to parse state file: %v", err)
	}

	if state.ProcessedFiles == nil {
		state.ProcessedFiles = make(map[string]ProcessedFile)
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

// IsProcessed checks if a file has already been processed
func (s *State) IsProcessed(filename string) bool {
	_, exists := s.ProcessedFiles[filename]
	return exists
}

// MarkProcessed marks a file as processed
func (s *State) MarkProcessed(filename, profileUsed, outputPath string) {
	s.ProcessedFiles[filename] = ProcessedFile{
		Filename:    filename,
		ProcessedAt: time.Now(),
		ProfileUsed: profileUsed,
	}
	s.LastRun = time.Now()
}

// GetProcessedFilesMap returns a map for quick lookup of processed files
func (s *State) GetProcessedFilesMap() map[string]bool {
	result := make(map[string]bool)
	for filename := range s.ProcessedFiles {
		result[filename] = true
	}
	return result
}

// GetProcessedCount returns the number of processed files
func (s *State) GetProcessedCount() int {
	return len(s.ProcessedFiles)
}

// SyncWithCard removes entries for files no longer on the card
// This keeps the state file clean and prevents stale entries
func (s *State) SyncWithCard(filesOnCard map[string]bool) int {
	removed := 0
	for filename := range s.ProcessedFiles {
		if !filesOnCard[filename] {
			delete(s.ProcessedFiles, filename)
			removed++
		}
	}
	return removed
}

// SetCardID sets an identifier for the current card
func (s *State) SetCardID(id string) {
	s.CardID = id
}

// Clear removes all state
func (s *State) Clear() int {
	count := len(s.ProcessedFiles)
	s.ProcessedFiles = make(map[string]ProcessedFile)
	s.CardID = ""
	s.LastRun = time.Time{}
	return count
}

// Stats returns statistics about the state
type Stats struct {
	ProcessedCount int
	LastRun        time.Time
	CardID         string
	FileSizeBytes  int64
}

// GetStats returns statistics about the state
func (s *State) GetStats() Stats {
	stats := Stats{
		ProcessedCount: len(s.ProcessedFiles),
		LastRun:        s.LastRun,
		CardID:         s.CardID,
	}

	if info, err := os.Stat(s.statePath); err == nil {
		stats.FileSizeBytes = info.Size()
	}

	return stats
}