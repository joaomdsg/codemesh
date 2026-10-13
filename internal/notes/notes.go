// Package notes keeps a reviewer's notes on units of a diff.
package notes

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"time"

	"github.com/joaomdsg/codemesh/internal/store"
)

// Note is a comment anchored to a review unit. UnitKey is the unit's key when
// the note was written; it changes when the unit's source does.
type Note struct {
	ID       string
	Repo     string
	Scope    string // "wt", or "pr:<n>@<headSHA>"
	File     string
	DeclID   string
	UnitKey  string
	Side     string // "+" or "-"
	Line     int
	Body     string
	Created  time.Time
	Resolved bool
}

// Outdated reports whether the unit a note was written on is gone from the
// review, or its source changed. keys holds the current review's unit keys.
// A note with no unit never goes stale.
func Outdated(n Note, keys map[string]bool) bool {
	return n.UnitKey != "" && !keys[n.UnitKey]
}

// Store keeps a repo's notes in a JSON file. Safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	all  []Note
}

// Open reads the file at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := store.ReadJSON(path, &s.all); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

// All returns the notes in the order they were added.
func (s *Store) All() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.all)
}

// Add stores n under a new ID and returns it.
func (s *Store) Add(n Note) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make([]byte, 6)
	rand.Read(b)
	n.ID = hex.EncodeToString(b)
	return n, s.write(append(slices.Clone(s.all), n))
}

// Resolve sets the resolved flag of the note with the given ID.
func (s *Store) Resolve(id string, resolved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return errors.New("notes: no note " + id)
	}
	next := slices.Clone(s.all)
	next[i].Resolved = resolved
	return s.write(next)
}

// Delete removes the note with the given ID, if any.
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
	return slices.IndexFunc(s.all, func(n Note) bool { return n.ID == id })
}

func (s *Store) write(next []Note) error {
	if err := store.WriteJSON(s.path, next); err != nil {
		return err
	}
	s.all = next
	return nil
}
