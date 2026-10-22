//go:build !linux && !windows

package switcher

import (
	"os"
)

func syncDir(string) error { return nil }

func secureDataDir(path string) error { return os.Chmod(path, 0o700) }

func secureTempFile(_ string, file *os.File, mode os.FileMode) error { return file.Chmod(mode) }

func atomicReplace(source, destination string) error { return os.Rename(source, destination) }
