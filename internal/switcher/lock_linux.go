//go:build linux

package switcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (s *Store) lock() (func() error, error) {
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, "switch.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open switcher lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("switcher lock is not a regular file")
		}
		return nil, errors.Join(err, f.Close())
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("set switcher lock permissions: %w", err), f.Close())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() error {
				unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				closeErr := f.Close()
				return errors.Join(unlockErr, closeErr)
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("switcher lock: %w", err), f.Close())
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(errors.New("another switcher is busy; try again shortly"), f.Close())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
