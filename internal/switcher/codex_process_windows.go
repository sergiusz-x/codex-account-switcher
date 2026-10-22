//go:build windows

package switcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func newCodexCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	resolved, err := exec.LookPath("codex")
	if err != nil {
		return nil, errors.New("Codex CLI was not found on PATH")
	}
	return commandForWindowsCodex(ctx, resolved, args...)
}

func newCodexCommandAt(ctx context.Context, executable string, args ...string) (*exec.Cmd, error) {
	return commandForWindowsCodex(ctx, executable, args...)
}

func commandForWindowsCodex(ctx context.Context, resolved string, args ...string) (*exec.Cmd, error) {
	extension := strings.ToLower(filepath.Ext(resolved))
	if extension == ".exe" {
		return exec.CommandContext(ctx, resolved, args...), nil
	}
	if extension != ".cmd" {
		return nil, fmt.Errorf("unsupported Codex launcher %s; use codex.exe or the standard npm launcher", extension)
	}
	packageScripts := []string{
		filepath.Join(filepath.Dir(resolved), "node_modules", "@openai", "codex", "bin", "codex.js"),
		filepath.Join(filepath.Dir(resolved), "..", "@openai", "codex", "bin", "codex.js"),
	}
	packageScript := ""
	for _, candidate := range packageScripts {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			packageScript, err = filepath.Abs(candidate)
			if err == nil {
				break
			}
		}
	}
	if packageScript == "" {
		return nil, errors.New("the Codex npm launcher does not contain its expected package script")
	}
	architecture := map[string]string{"amd64": "x86_64-pc-windows-msvc", "arm64": "aarch64-pc-windows-msvc"}[runtime.GOARCH]
	if architecture == "" {
		return nil, errors.New("the Codex npm package does not support this Windows architecture")
	}
	packageRoot := filepath.Join(filepath.Dir(packageScript), "..", "..", "..")
	native := filepath.Join(packageRoot, "@openai", "codex-win32-x64", "vendor", architecture, "bin", "codex.exe")
	if runtime.GOARCH == "arm64" {
		native = filepath.Join(packageRoot, "@openai", "codex-win32-arm64", "vendor", architecture, "bin", "codex.exe")
	}
	info, err := os.Stat(native)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("the Codex npm installation does not contain its Windows executable")
	}
	return exec.CommandContext(ctx, native, args...), nil
}
