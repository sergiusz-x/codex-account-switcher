//go:build linux

package switcher

import (
	"errors"
	"os"
)

func secureDataDir(path string) error { return os.Chmod(path, 0o700) }

func secureTempFile(_ string, file *os.File, mode os.FileMode) error { return file.Chmod(mode) }

func atomicReplace(source, destination string) error { return os.Rename(source, destination) }

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
