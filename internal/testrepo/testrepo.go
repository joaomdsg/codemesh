// Package testrepo builds throwaway git repositories for tests.
package testrepo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// New commits base into a fresh repo under t.TempDir, then writes head over
// it uncommitted, and returns the repo directory.
func New(t testing.TB, base, head map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := build(dir, base, head); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Make is New for shared fixtures built once outside any test. The caller
// owns the returned directory.
func Make(base, head map[string]string) (string, error) {
	dir, err := os.MkdirTemp("", "codemesh-repo-")
	if err != nil {
		return "", err
	}
	return dir, build(dir, base, head)
}

func build(dir string, base, head map[string]string) error {
	if err := Write(dir, base); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-qm", "base"}} {
		if err := Git(dir, args...); err != nil {
			return err
		}
	}
	return Write(dir, head)
}

// Write writes files, relative to dir.
func Write(dir string, files map[string]string) error {
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Git runs git in dir with a fixed identity and no user or system config.
func Git(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return nil
}
