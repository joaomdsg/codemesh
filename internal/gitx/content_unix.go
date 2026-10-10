//go:build unix

package gitx

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// Content reads path as git stores it: a link as its target, never what it
// points to, which may be outside the tree, huge or endless. Anything else
// that is not a regular file has no content. The type is read from the open
// file, so nothing swapped in after the check is read instead.
func Content(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		target, err := os.Readlink(path)
		return []byte(target), err
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, err
	}
	return io.ReadAll(f)
}
