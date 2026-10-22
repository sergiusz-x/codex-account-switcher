//go:build windows

package switcher

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func (s *Store) lock() (func() error, error) {
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	path := s.root + string(os.PathSeparator) + "switch.lock"
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open switcher lock: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, errors.Join(fmt.Errorf("inspect switcher lock: %w", err), windows.CloseHandle(handle))
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return nil, errors.Join(errors.New("switcher lock is not a regular file"), windows.CloseHandle(handle))
	}
	if err := secureHandle(handle, false); err != nil {
		return nil, errors.Join(fmt.Errorf("secure switcher lock: %w", err), windows.CloseHandle(handle))
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(errors.New("wrap switcher lock handle"), windows.CloseHandle(handle))
	}
	var overlapped windows.Overlapped
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			return func() error {
				unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
				closeErr := file.Close()
				return errors.Join(unlockErr, closeErr)
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_FAILED) {
			return nil, errors.Join(fmt.Errorf("switcher lock: %w", err), file.Close())
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(errors.New("another switcher is busy; try again shortly"), file.Close())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
