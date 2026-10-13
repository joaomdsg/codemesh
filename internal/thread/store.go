package thread

import (
	"errors"
	"io/fs"
	"slices"
	"sync"

	"github.com/joaomdsg/codemesh/internal/store"
)

// Store keeps a repo's threads in a JSON file. Safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	all  []Thread
}

// Open reads the file at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := store.ReadJSON(path, &s.all); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

// All returns the threads in the order they were first stored.
func (s *Store) All() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Thread, len(s.all))
	for i, t := range s.all {
		out[i] = clone(t)
	}
	return out
}

// Get returns a copy of the thread with the given ID.
func (s *Store) Get(id string) (Thread, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i >= 0 {
		return clone(s.all[i]), true
	}
	return Thread{}, false
}

// Put stores t, replacing the thread with the same ID, and writes the file.
func (s *Store) Put(t Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := slices.Clone(s.all)
	if i := s.index(t.ID); i >= 0 {
		next[i] = clone(t)
	} else {
		next = append(next, clone(t))
	}
	return s.write(next)
}

// Delete removes the thread with the given ID, if any.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return nil
	}
	return s.write(slices.Delete(slices.Clone(s.all), i, i+1))
}

func (s *Store) index(id string) int {
	return slices.IndexFunc(s.all, func(t Thread) bool { return t.ID == id })
}

func (s *Store) write(next []Thread) error {
	if err := store.WriteJSON(s.path, next); err != nil {
		return err
	}
	s.all = next
	return nil
}

func clone(t Thread) Thread {
	t.Hops = slices.Clone(t.Hops)
	return t
}
