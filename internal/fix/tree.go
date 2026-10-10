package fix

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
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
	file, _ := inside(t.mod, filepath.Join(t.wt, p))
	var cs []Change
	for _, h := range hunks(before, after) {
		cs = append(cs, Change{Path: p, File: file, Start: h.start, Old: clip(h.old, false), New: clip(h.new, false)})
	}
	return cs
}

type hunk struct {
	start    int // first line of the old side shown
	old, new string
}

// hunks diffs two versions of a file with git, one hunk per separate change
// with two lines of context: one region from the first change to the last
// would bury two small edits far apart in everything between them.
func hunks(a, b string) []hunk {
	dir, err := os.MkdirTemp("", "codemesh-diff-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	old, new := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if os.WriteFile(old, []byte(a), 0o600) != nil || os.WriteFile(new, []byte(b), 0o600) != nil {
		return nil
	}
	// --no-index exits 1 when the files differ; the diff is still on stdout.
	out, _ := exec.Command("git", "diff", "--no-index", "--no-color", "-U2", old, new).Output()
	files, _, err := gitdiff.Parse(bytes.NewReader(out))
	if err != nil || len(files) == 0 {
		return nil
	}
	var hs []hunk
	for _, f := range files[0].TextFragments {
		var o, n strings.Builder
		for _, l := range f.Lines {
			if l.Op != gitdiff.OpAdd {
				o.WriteString(l.Line)
			}
			if l.Op != gitdiff.OpDelete {
				n.WriteString(l.Line)
			}
		}
		hs = append(hs, hunk{start: int(f.OldPosition), old: o.String(), new: n.String()})
	}
	return hs
}

// inside gives p relative to root, slash-separated, when p lies within it.
func inside(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// shellKind sorts a shell command into a lane by its first word: reading and
// searching tools read, everything else runs.
func shellKind(cmd string, reads []Read) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return kindExec
	}
	switch filepath.Base(fields[0]) {
	case "grep", "rg", "find", "ls", "tree", "fd", "ag":
		return kindSearch
	case "cat", "head", "tail", "sed", "awk", "nl", "less", "wc", "bat":
		if len(reads) > 0 {
			return kindRead
		}
	}
	return kindExec
}
