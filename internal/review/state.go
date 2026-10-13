package review

import (
	"errors"
	"io/fs"
	"slices"
	"sync"

	"github.com/joaomdsg/codemesh/internal/store"
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
func OpenState(path string) (*State, error) { return OpenStateFrom(path, "") }

// OpenStateFrom is OpenState that, while path does not exist, starts from the
// keys in the legacy file instead. The legacy file is left as it is.
func OpenStateFrom(path, legacy string) (*State, error) {
	s := &State{path: path, done: map[string]bool{}}
	var keys []string
	err := store.ReadJSON(path, &keys)
	if errors.Is(err, fs.ErrNotExist) && legacy != "" {
		err = store.ReadJSON(legacy, &keys)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
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
	return store.WriteJSON(s.path, keys)
}
