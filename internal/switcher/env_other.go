//go:build !windows

package switcher

func environmentKeyEqual(a, b string) bool { return a == b }
