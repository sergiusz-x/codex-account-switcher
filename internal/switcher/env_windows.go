//go:build windows

package switcher

import "strings"

func environmentKeyEqual(a, b string) bool { return strings.EqualFold(a, b) }
