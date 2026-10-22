//go:build !linux

package switcher

func syncDir(string) error { return nil }
