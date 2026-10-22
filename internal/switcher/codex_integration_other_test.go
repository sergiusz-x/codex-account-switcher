//go:build !windows

package switcher

func codexIntegrationACLs(string) string { return "not-applicable" }

func secureCodexIntegrationDir(path string) error { return secureDataDir(path) }
