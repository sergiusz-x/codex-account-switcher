package version

import (
	_ "embed"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

//go:embed VERSION
var version string

func Version() string {
	return strings.TrimSpace(version)
}

func String() string {
	parts := []string{runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH}
	if source := sourceDescription(); source != "" {
		parts = append(parts, source)
	}
	return fmt.Sprintf("%s (%s)", Version(), strings.Join(parts, "; "))
}

func sourceDescription() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision == "" {
		if modified {
			return "uncommitted source"
		}
		return ""
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if modified {
		return "commit " + revision + " (dirty)"
	}
	return "commit " + revision
}
