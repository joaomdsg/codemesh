package ghx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/ghx"
)

// fakeGH puts testdata/fake-gh on PATH under the name gh.
func fakeGH(t *testing.T) {
	t.Helper()
	src, err := filepath.Abs("testdata/fake-gh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(src, filepath.Join(bin, "gh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestListParsesPRs(t *testing.T) {
	fakeGH(t)
	got, err := ghx.CLI{}.List(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d PRs, want 2", len(got))
	}
	want := ghx.PR{
		Number: 7, Title: "Add cache", Author: "ana",
		HeadRef: "feat/cache", BaseRef: "main",
		HeadOID: "1111111111111111111111111111111111111111",
		BaseOID: "2222222222222222222222222222222222222222",
		URL:     "https://github.com/o/r/pull/7", Draft: true,
		UpdatedAt:    time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC),
		ChangedFiles: 3, Additions: 40, Deletions: 5,
		ReviewDecision: "REVIEW_REQUIRED",
	}
	if got[0] != want {
		t.Errorf("PR 7 = %+v, want %+v", got[0], want)
	}
	if got[1].Number != 9 || got[1].Author != "bo" || got[1].Draft {
		t.Errorf("PR 9 = %+v", got[1])
	}
}

func TestListNoGH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ghx.CLI{}.List(context.Background(), t.TempDir())
	if !errors.Is(err, ghx.ErrNoGH) {
		t.Fatalf("err = %v, want ErrNoGH", err)
	}
}

func TestListNotAuthed(t *testing.T) {
	fakeGH(t)
	t.Setenv("FAKE_GH_EXIT", "4")
	_, err := ghx.CLI{}.List(context.Background(), t.TempDir())
	if !errors.Is(err, ghx.ErrNotAuthed) {
		t.Fatalf("err = %v, want ErrNotAuthed", err)
	}
}

func TestListOtherFailure(t *testing.T) {
	fakeGH(t)
	t.Setenv("FAKE_GH_EXIT", "1")
	_, err := ghx.CLI{}.List(context.Background(), t.TempDir())
	if err == nil || errors.Is(err, ghx.ErrNotAuthed) || errors.Is(err, ghx.ErrNoGH) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
}
