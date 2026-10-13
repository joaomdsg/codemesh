// Package handoff collects what the user wants a coding agent to act on and
// renders it as one prompt.
package handoff

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"slices"
	"sync"

	"github.com/joaomdsg/codemesh/internal/store"
)

// Kind says where an item came from.
type Kind string

const (
	Finding    Kind = "finding"
	Prognosis  Kind = "prognosis"
	Note       Kind = "note"
	Thread     Kind = "thread"
	Introduced Kind = "introduced" // a finding a PR introduced
)

// Item is one thing to hand off. Its text is copied from the source when the
// item is added, so the prompt does not change under the user. Key is the
// source's key at that moment, kept so a stale item can be spotted.
type Item struct {
	ID    string
	Kind  Kind
	Repo  string // repo ID
	Scope string // "wt", or "pr:<n>@<headSHA>"
	Title string
	Where string // file:line
	Body  string
	Steps []string
	Check string
	Code  []string
	Key   string
}

// Basket is the ordered list of items, and the line that opens the prompt.
type Basket struct {
	Intro string
	Items []Item
}

// Store keeps the basket in a JSON file. Safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	b    Basket
}

// Open reads the file at path; a missing file is an empty basket.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := store.ReadJSON(path, &s.b); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

// Basket returns a copy of the current basket.
func (s *Store) Basket() Basket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.b)
}

// Add appends it under a new ID and returns it.
func (s *Store) Add(it Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make([]byte, 6)
	rand.Read(b)
	it.ID = hex.EncodeToString(b)
	next := clone(s.b)
	next.Items = append(next.Items, it)
	return it, s.write(next)
}

// Remove deletes the item with the given ID, if any.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return nil
	}
	next := clone(s.b)
	next.Items = slices.Delete(next.Items, i, i+1)
	return s.write(next)
}

// Move puts the item at position to, clamped to the list.
func (s *Store) Move(id string, to int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return errors.New("handoff: no item " + id)
	}
	next := clone(s.b)
	it := next.Items[i]
	next.Items = slices.Delete(next.Items, i, i+1)
	to = max(0, min(to, len(next.Items)))
	next.Items = slices.Insert(next.Items, to, it)
	return s.write(next)
}

// SetIntro replaces the line that states the prompt's goal.
func (s *Store) SetIntro(intro string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.b)
	next.Intro = intro
	return s.write(next)
}

func (s *Store) index(id string) int {
	return slices.IndexFunc(s.b.Items, func(it Item) bool { return it.ID == id })
}

func (s *Store) write(next Basket) error {
	if err := store.WriteJSON(s.path, next); err != nil {
		return err
	}
	s.b = next
	return nil
}

func clone(b Basket) Basket {
	b.Items = slices.Clone(b.Items)
	return b
}
