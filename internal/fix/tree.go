package fix

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Change is one file a step left different, as the lines it replaced and
// their replacement, with a little context. Start is the first line shown.
type Change struct {
	Path     string // relative to the worktree
	File     string // relative to the module, when in it
	Start    int
	Old, New string
}

// tree watches a worktree so each step's effect on it is known whatever tool
// made it: Claude reads and edits through the shell as often as through its
// own tools.
type tree struct {
	wt, mod string

	mu   sync.Mutex
	seen map[string]string // worktree path → contents after the last step that changed it
}

func newTree(wt, mod string) *tree { return &tree{wt: wt, mod: mod, seen: map[string]string{}} }

// changes reports the files that differ from what the last call saw, against
// the commit for files no step has changed yet.
func (t *tree) changes() []Change {
	if t == nil {
		return nil
	}
	out, err := exec.Command("git", "-C", t.wt, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var cs []Change
	dirty := map[string]bool{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 4 {
			continue
		}
		p := rec[3:]
		dirty[p] = true
		cs = append(cs, t.diff(p)...)
	}
	// A file put back to its committed contents is a change too.
	for p := range t.seen {
		if !dirty[p] {
			cs = append(cs, t.diff(p)...)
		}
	}
	return cs
}

func (t *tree) diff(p string) []Change {
	before, ok := t.seen[p]
	if !ok {
		head, _ := exec.Command("git", "-C", t.wt, "show", "HEAD:"+p).Output()
		before = string(head)
	}
	data, _ := os.ReadFile(filepath.Join(t.wt, p))
	after := string(data)
	if after == before {
		return nil
	}
	t.seen[p] = after
	start, old, new := hunk(before, after)
	c := Change{Path: p, Start: start, Old: clip(old, false), New: clip(new, false)}
	if rel, err := filepath.Rel(t.mod, filepath.Join(t.wt, p)); err == nil && !strings.HasPrefix(rel, "..") {
		c.File = filepath.ToSlash(rel)
	}
	return []Change{c}
}

// hunk trims the lines two versions share at both ends, keeping two of them
// as context, and returns the first kept line's number with what is left.
func hunk(a, b string) (start int, old, new string) {
	A, B := strings.Split(a, "\n"), strings.Split(b, "\n")
	p := 0
	for p < len(A) && p < len(B) && A[p] == B[p] {
		p++
	}
	q := 0
	for q < len(A)-p && q < len(B)-p && A[len(A)-1-q] == B[len(B)-1-q] {
		q++
	}
	from := max(0, p-2)
	q = max(0, q-2)
	return from + 1, strings.Join(A[from:len(A)-q], "\n"), strings.Join(B[from:len(B)-q], "\n")
}

// named lists the module files a shell command names, so a `cat` or `sed -n`
// counts as reading them. It reads arguments, not the shell: a path built
// at run time is missed.
func (t *tree) named(cmd string) []string {
	if t == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(cmd, func(r rune) bool { return strings.ContainsRune(" \t\n;|&<>()'\"`=,", r) }) {
		p := f
		if !filepath.IsAbs(p) {
			p = filepath.Join(t.mod, p)
		}
		rel, err := filepath.Rel(t.mod, p)
		if err != nil || strings.HasPrefix(rel, "..") || seen[rel] {
			continue
		}
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			seen[rel] = true
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// shellKind sorts a shell command into a lane by its first word: reading and
// searching tools read, everything else runs.
func shellKind(cmd string, reads []string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return Exec
	}
	switch filepath.Base(fields[0]) {
	case "grep", "rg", "find", "ls", "tree", "fd", "ag":
		return Search
	case "cat", "head", "tail", "sed", "awk", "nl", "less", "wc", "bat":
		if len(reads) > 0 {
			return Read
		}
	}
	return Exec
}
