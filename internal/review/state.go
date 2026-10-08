package review

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// State is the set of unit keys a reviewer marked reviewed, kept in a JSON
// file. Keys carry a source hash, so an edited unit comes back unreviewed.
// Safe for concurrent use.
type State struct {
	path string
	mu   sync.Mutex
	done map[string]bool
}

// OpenState reads the state file at path; a missing file is an empty state.
func OpenState(path string) (*State, error) {
	s := &State{path: path, done: map[string]bool{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		s.done[k] = true
	}
	return s, nil
}

// Reviewed reports whether key is marked reviewed.
func (s *State) Reviewed(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done[key]
}

// Set marks or unmarks key and writes the file.
func (s *State) Set(key string, done bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if done {
		s.done[key] = true
	} else {
		delete(s.done, key)
	}
	keys := make([]string, 0, len(s.done))
	for k := range s.done {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	data, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	// Write then rename, so a crash never leaves a torn file.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
