// Package thread follows the blast radius of a declaration: from a root, hop
// to one of its callers or users, then to theirs, keeping the path.
package thread

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
)

// Via is how a hop reaches the decl before it.
type Via string

const (
	Caller Via = "caller"
	User   Via = "user"
)

// Thread is a path through the graph. The root is not a hop.
type Thread struct {
	ID      string
	Scope   string // "wt", or "pr:<n>@<headSHA>"
	Root    string // decl ID
	RootKey string
	Hops    []Hop
	Created time.Time
}

// Hop is one decl on the path. Key is the decl's review key when it was
// followed, so Resolve can tell a moved decl from an unchanged one.
type Hop struct {
	DeclID string
	Via    Via
	From   string // decl ID of the previous step
	Key    string
	Note   string
}

// Candidate is a decl the thread could follow next.
type Candidate struct {
	DeclID string
	Via    Via
	From   string
}

// Status is a hop's state against a newer snapshot.
type Status string

const (
	Current Status = ""
	Moved   Status = "moved" // the decl exists with a different key
	Gone    Status = "gone"  // the decl no longer exists
)

// New starts a thread at root, with a random ID.
func New(scope, root, rootKey string) Thread {
	return Thread{ID: newID(), Scope: scope, Root: root, RootKey: rootKey, Created: time.Now().UTC()}
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Next returns the callers and users of the thread's last step that are not
// already on its path. Production code comes before tests, then the decls
// with more callers of their own, then by ID. A decl that is both caller and
// user is listed once, as a caller.
func Next(s *code.Snapshot, t Thread) []Candidate {
	last := t.Root
	if n := len(t.Hops); n > 0 {
		last = t.Hops[n-1].DeclID
	}
	d := s.Decl(last)
	if d == nil {
		return nil
	}
	onPath := map[string]bool{t.Root: true}
	for _, h := range t.Hops {
		onPath[h.DeclID] = true
	}
	var out []Candidate
	for _, via := range []struct {
		ids []string
		via Via
	}{{d.Callers, Caller}, {d.Users, User}} {
		for _, id := range via.ids {
			if onPath[id] || s.Decl(id) == nil {
				continue
			}
			onPath[id] = true
			out = append(out, Candidate{DeclID: id, Via: via.via, From: last})
		}
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		da, db := s.Decl(a.DeclID), s.Decl(b.DeclID)
		return cmp.Or(
			cmpBool(da.Test, db.Test),
			cmp.Compare(len(db.Callers), len(da.Callers)),
			cmp.Compare(a.DeclID, b.DeclID),
		)
	})
	return out
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}

// Follow appends c to the path; key is the decl's current review key.
func Follow(t *Thread, c Candidate, key string) {
	t.Hops = append(t.Hops, Hop{DeclID: c.DeclID, Via: c.Via, From: c.From, Key: key})
}

// Trim keeps the first n hops.
func Trim(t *Thread, n int) {
	if n < len(t.Hops) {
		t.Hops = t.Hops[:max(n, 0)]
	}
}

// Resolve compares the thread with s. The result has the root's status
// first, then one per hop. key computes a decl's review key in s.
func Resolve(s *code.Snapshot, t Thread, key func(*code.Decl) string) []Status {
	status := func(id, old string) Status {
		d := s.Decl(id)
		switch {
		case d == nil:
			return Gone
		case old != "" && key(d) != old:
			return Moved
		}
		return Current
	}
	out := []Status{status(t.Root, t.RootKey)}
	for _, h := range t.Hops {
		out = append(out, status(h.DeclID, h.Key))
	}
	return out
}
