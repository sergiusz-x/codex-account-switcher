//go:build windows

package switcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestWindowsCodexCommandResolution(t *testing.T) {
	root := filepath.Join(t.TempDir(), "npm space ż &")
	shimDir := filepath.Join(root, "bin")
	packageScript := filepath.Join(shimDir, "node_modules", "@openai", "codex", "bin", "codex.js")
	if err := os.MkdirAll(filepath.Dir(packageScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packageScript, []byte("// synthetic npm entry point"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(shimDir, "codex.cmd")
	if err := os.WriteFile(launcher, []byte("synthetic npm launcher"), 0o600); err != nil {
		t.Fatal(err)
	}
	architecture := "x86_64-pc-windows-msvc"
	packageName := "codex-win32-x64"
	if runtime.GOARCH == "arm64" {
		architecture = "aarch64-pc-windows-msvc"
		packageName = "codex-win32-arm64"
	}
	native := filepath.Join(shimDir, "node_modules", "@openai", packageName, "vendor", architecture, "bin", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("synthetic native executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"app-server", "--listen", "stdio://", "-c", `cli_auth_credentials_store="file"`, `value & spaces %`}
	cmd, err := commandForWindowsCodex(context.Background(), launcher, args...)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{native}, args...)
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("npm package executable args = %#v, want %#v", cmd.Args, want)
	}

	native = filepath.Join(root, "codex.exe")
	cmd, err = commandForWindowsCodex(context.Background(), native, args...)
	if err != nil {
		t.Fatal(err)
	}
	want = append([]string{native}, args...)
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("native command args = %#v, want %#v", cmd.Args, want)
	}
	if _, err := commandForWindowsCodex(context.Background(), filepath.Join(root, "codex.ps1"), args...); err == nil {
		t.Fatal("PowerShell script launcher was accepted as an executable")
	}
}

func TestWindowsEnvironmentOverridesAreCaseInsensitive(t *testing.T) {
	login := loginEnvironment([]string{
		"cOdEx_HoMe=real-home",
		"OPENAI_api_key=synthetic-key",
		"OpenAI_API_KEY=second-synthetic-key",
		"Path=C:\\Windows\\System32",
	}, `C:\temp\private home`)
	if len(login) != 2 || login[0] != `Path=C:\Windows\System32` || login[1] != `CODEX_HOME=C:\temp\private home` {
		t.Fatalf("isolated login environment = %#v", login)
	}
	usage := filterUsageEnvironment([]string{
		"CODEX_HOME=real-home",
		"cOdEx_bAsE_uRl=https://example.invalid",
		"hTtPs_PrOxY=http://127.0.0.1:9999",
		"Codex_Refresh_Token_Url_Override=https://example.invalid/token",
		"SystemRoot=C:\\Windows",
	}, `C:\temp\private home`)
	if len(usage) != 2 || usage[0] != `SystemRoot=C:\Windows` || usage[1] != `CODEX_HOME=C:\temp\private home` {
		t.Fatalf("isolated usage environment = %#v", usage)
	}
}
