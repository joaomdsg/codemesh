package thread_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/thread"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshot builds target, called by zbusy (which target also calls, and 3 more),
// quiet and a test, and used by user and quiet.
func snapshot() *code.Snapshot {
	b := code.NewBuilder(code.Go, "m", "/m")
	p := b.Package(code.Package{Path: "m", Rel: ".", Name: "m"})
	f := b.File(p, code.File{Path: "a.go"})
	tf := b.File(p, code.File{Path: "a_test.go", Test: true})
	decl := func(f *code.File, name string, test bool) *code.Decl {
		return b.Decl(f, code.Decl{ID: "m." + name, Name: name, Kind: code.Func, Test: test})
	}
	target := decl(f, "target", false)
	zbusy := decl(f, "zbusy", false)
	quiet := decl(f, "quiet", false)
	user := decl(f, "user", false)
	test := decl(tf, "TestTarget", true)
	b.Link(zbusy, target, code.Caller)
	b.Link(quiet, target, code.Caller)
	b.Link(test, target, code.Caller)
	b.Link(user, target, code.User)
	b.Link(target, zbusy, code.Caller)
	b.Link(quiet, target, code.User)
	for _, n := range []string{"c1", "c2", "c3"} {
		b.Link(decl(f, n, false), zbusy, code.Caller)
	}
	for _, n := range []string{"t1", "t2", "t3", "t4"} {
		b.Link(decl(tf, n, true), test, code.Caller)
	}
	return b.Finish()
}

func key(d *code.Decl) string { return d.ID + "@" + d.Name }

func ids(cs []thread.Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.DeclID)
	}
	return out
}

func TestNext_ranksProductionBeforeTestsThenByCallerCount(t *testing.T) {
	t.Parallel()
	s := snapshot()
	th := thread.New("wt", "m.target", "m.target@target")

	got := thread.Next(s, th)

	assert.Equal(t, []string{"m.zbusy", "m.quiet", "m.user", "m.TestTarget"}, ids(got),
		"production by caller count (zbusy sorts last by ID), tests last despite 4 callers, tests last despite 4 callers")
	assert.Equal(t, thread.Caller, got[0].Via)
	assert.Equal(t, thread.User, got[2].Via)
	assert.Equal(t, "m.target", got[0].From)
}

func TestNext_listsACallerAndUserOnceAsCaller(t *testing.T) {
	t.Parallel()
	got := thread.Next(snapshot(), thread.New("wt", "m.target", ""))

	var quiet []thread.Candidate
	for _, c := range got {
		if c.DeclID == "m.quiet" {
			quiet = append(quiet, c)
		}
	}
	require.Len(t, quiet, 1)
	assert.Equal(t, thread.Caller, quiet[0].Via)
}

func TestFollow_extendsThePathAndNextSkipsWhatIsOnIt(t *testing.T) {
	t.Parallel()
	s := snapshot()
	th := thread.New("wt", "m.target", "m.target@target")
	cands := thread.Next(s, th)

	thread.Follow(&th, cands[0], "m.zbusy@zbusy")

	require.Len(t, th.Hops, 1)
	assert.Equal(t, thread.Hop{DeclID: "m.zbusy", Via: thread.Caller, From: "m.target", Key: "m.zbusy@zbusy"}, th.Hops[0])
	next := thread.Next(s, th)
	assert.Equal(t, []string{"m.c1", "m.c2", "m.c3"}, ids(next), "zbusy is also called by the root, which is on the path")

	thread.Follow(&th, next[0], "")
}

func TestTrim_cutsThePathToItsFirstNHops(t *testing.T) {
	t.Parallel()
	th := thread.New("wt", "m.target", "")
	thread.Follow(&th, thread.Candidate{DeclID: "m.zbusy", Via: thread.Caller, From: "m.target"}, "")
	thread.Follow(&th, thread.Candidate{DeclID: "m.c1", Via: thread.Caller, From: "m.zbusy"}, "")

	thread.Trim(&th, 1)
	require.Len(t, th.Hops, 1)
	assert.Equal(t, "m.zbusy", th.Hops[0].DeclID)

	thread.Trim(&th, 5)
	assert.Len(t, th.Hops, 1, "past the end changes nothing")
	thread.Trim(&th, 0)
	assert.Empty(t, th.Hops)
}

func TestResolve_marksMovedAndGoneAgainstANewerSnapshot(t *testing.T) {
	t.Parallel()
	th := thread.New("wt", "m.target", "m.target@target")
	thread.Follow(&th, thread.Candidate{DeclID: "m.zbusy", Via: thread.Caller, From: "m.target"}, "m.zbusy@zbusy")
	thread.Follow(&th, thread.Candidate{DeclID: "m.gone", Via: thread.Caller, From: "m.zbusy"}, "m.gone@gone")
	thread.Follow(&th, thread.Candidate{DeclID: "m.quiet", Via: thread.Caller, From: "m.gone"}, "m.quiet@old")

	got := thread.Resolve(snapshot(), th, key)

	assert.Equal(t, []thread.Status{thread.Current, thread.Current, thread.Gone, thread.Moved}, got,
		"root, then each hop")
}

func TestStore_roundTripsAndReplacesByID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "repos", "r", "threads.json")
	s, err := thread.Open(path)
	require.NoError(t, err)
	assert.Empty(t, s.All())

	th := thread.New("pr:7@abc", "m.target", "m.target@target")
	th.Created = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	thread.Follow(&th, thread.Candidate{DeclID: "m.zbusy", Via: thread.User, From: "m.target"}, "m.zbusy@zbusy")
	require.NoError(t, s.Put(th))

	re, err := thread.Open(path)
	require.NoError(t, err)
	got, ok := re.Get(th.ID)
	require.True(t, ok)
	assert.Equal(t, th, got)

	thread.Trim(&th, 0)
	require.NoError(t, re.Put(th))
	assert.Len(t, re.All(), 1, "same ID replaces")
	got, _ = re.Get(th.ID)
	assert.Empty(t, got.Hops)

	other := thread.New("wt", "m.quiet", "")
	require.NoError(t, re.Put(other))
	assert.NotEqual(t, th.ID, other.ID)
	require.NoError(t, re.Delete(th.ID))
	assert.Equal(t, []thread.Thread{other}, re.All())
}

func TestStore_getReturnsACopy(t *testing.T) {
	t.Parallel()
	s, err := thread.Open(filepath.Join(t.TempDir(), "threads.json"))
	require.NoError(t, err)
	th := thread.New("wt", "m.target", "")
	thread.Follow(&th, thread.Candidate{DeclID: "m.zbusy", Via: thread.Caller, From: "m.target"}, "")
	require.NoError(t, s.Put(th))

	got, _ := s.Get(th.ID)
	thread.Trim(&got, 0)
	got.Hops = append(got.Hops, thread.Hop{DeclID: "x"})

	again, _ := s.Get(th.ID)
	assert.Equal(t, "m.zbusy", again.Hops[0].DeclID)
}
