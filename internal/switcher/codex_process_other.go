//go:build !windows

package switcher

import (
	"context"
	"os/exec"
)

func newCodexCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "codex", args...), nil
}

func newCodexCommandAt(ctx context.Context, executable string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, executable, args...), nil
}
