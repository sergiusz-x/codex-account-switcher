//go:build !windows

package switcher

import "testing"

func requireCodexDaemonSupport(*testing.T) {}

func codexIntegrationACLs(string) string { return "not-applicable" }

func secureCodexIntegrationDir(path string) error { return secureDataDir(path) }
