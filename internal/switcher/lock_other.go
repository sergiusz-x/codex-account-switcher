//go:build !linux && !windows

package switcher

import "errors"

func (s *Store) lock() (func() error, error) {
	return nil, errors.New("file locking is only implemented for Linux in this version")
}
