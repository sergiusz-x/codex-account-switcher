package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestStringIncludesVersionAndRuntime(t *testing.T) {
	got := String()
	for _, want := range []string{Version(), runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(got, want) {
			t.Fatalf("String() = %q, want it to contain %q", got, want)
		}
	}
}
