package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ReadJSON decodes the file at path into v. A missing file returns an error
// that satisfies errors.Is(err, fs.ErrNotExist).
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteJSON replaces the file at path with v, creating parent directories.
// It writes a uniquely named temp file beside it and renames, so a crash
// leaves the old contents or the new, never a torn file, and concurrent
// writers cannot interleave.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
