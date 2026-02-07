package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// InstalledBinary tracks a locally installed binary.
type InstalledBinary struct {
	Name       string    `json:"name"`
	Package    string    `json:"package"`
	Version    string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

// State holds local gomanager state.
type State struct {
	Installed map[string]InstalledBinary `json:"installed"`
}

func statePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	dir := filepath.Join(configDir, "gomanager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create config directory: %w", err)
	}
	return filepath.Join(dir, "installed.json"), nil
}

// Load reads the state from disk.
func Load() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{Installed: make(map[string]InstalledBinary)}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Installed == nil {
		s.Installed = make(map[string]InstalledBinary)
	}
	return &s, nil
}

// Save writes the state to disk.
func (s *State) Save() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// MarkInstalled records a binary as installed.
func (s *State) MarkInstalled(name, pkg, version string) {
	s.Installed[name] = InstalledBinary{
		Name:        name,
		Package:     pkg,
		Version:     version,
		InstalledAt: time.Now(),
	}
}

// Remove removes a binary from the installed list.
func (s *State) Remove(name string) {
	delete(s.Installed, name)
}

