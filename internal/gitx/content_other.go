//go:build !unix

package gitx

import "os"

// Content reads path as git stores it: a link as its target, never what it
// points to. Anything else that is not a regular file has no content.
func Content(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return nil, err
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return []byte(target), err
	case !info.Mode().IsRegular():
		return nil, nil
	}
	return os.ReadFile(path)
}
