package switcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Opt in explicitly: this starts only a temporary daemon with synthetic credentials.
// All authentication and model endpoints point to an unused loopback port.
func TestRealCodexAccountRoundTrip(t *testing.T) {
	if os.Getenv("SWITCHER_REAL_CODEX") != "1" {
		t.Skip("set SWITCHER_REAL_CODEX=1 to test the installed Codex in a temporary home")
	}
	binary := os.Getenv("SWITCHER_CODEX_BINARY")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("set SWITCHER_CODEX_BINARY to the absolute path of the isolated Codex executable or npm launcher")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("isolated Codex executable is unavailable: %v", err)
	}
	home, err := newSyntheticCodexHome(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := secureCodexIntegrationDir(home); err != nil {
		t.Fatal(err)
	}
	store := NewStore(home)
	for _, directory := range []string{"app-server-daemon", "app-server-control"} {
		path := filepath.Join(store.codexHome, directory)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatalf("create private Codex test directory: %v", err)
		}
		if err := secureCodexIntegrationDir(path); err != nil {
			t.Fatalf("protect private Codex test directory: %v", err)
		}
	}
	writeTestAuth(t, filepath.Join(store.codexHome, "config.toml"), []byte(`cli_auth_credentials_store = "file"
chatgpt_base_url = "http://127.0.0.1:9/"
openai_base_url = "http://127.0.0.1:9/"
check_for_update_on_startup = false
[analytics]
enabled = false
[features]
code_mode_host = false
`))
	writeTestAuth(t, filepath.Join(store.codexHome, "app-server-daemon", "settings.json"),
		[]byte(`{"shutdownGraceSeconds":0,"updater":{"autoUpdateEnabled":false}}`))
	profiles := make([]Profile, 0, 2)
	for _, name := range []string{"a", "b"} {
		writeTestAuth(t, store.authPath(), realCodexSyntheticAuth(t, name))
		profile, err := store.SaveCurrent("Synthetic "+name, false)
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, profile)
	}
	client := codexClient{home: store.codexHome, binary: binary, environment: filterUsageEnvironment(os.Environ(), store.codexHome)}
	// Register cleanup before starting, including partial-start failure paths.
	t.Cleanup(func() {
		if _, err := client.lifecycle("stop"); err != nil {
			t.Error("stop isolated test daemon:", err)
		}
	})
	if err := bootstrapSyntheticDaemon(client); err != nil {
		t.Fatal("bootstrap isolated test daemon:", err)
	}
	if _, err := client.lifecycle("start"); err != nil {
		t.Fatal("start isolated test daemon:", err)
	}
	waitForCodexDaemon(t, client)
	// Reproduce the original bug: the selected file changes while the server
	// retains the previous account, even across a fresh switcher invocation.
	if _, err := store.Switch(profiles[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SwitchAccount(profiles[0].ID, false); !errors.Is(err, ErrServerRestartRequired) {
		t.Fatalf("stale real server was not detected: %v; daemon probe: %s", err, probeCodexDaemon(client, "version"))
	}
	for _, profile := range []Profile{profiles[0], profiles[1], profiles[0]} {
		result, err := store.SwitchAccount(profile.ID, true)
		if err != nil || !result.ServerVerified {
			t.Fatalf("real Codex round trip: verified=%v err=%v", result.ServerVerified, err)
		}
	}
	if err := testRealCodexUsage(binary); err != nil {
		t.Fatal("isolated Codex JSONL status test:", err)
	}
}

func probeCodexDaemon(client codexClient, action string) string {
	ctx, cancel := client.deadline()
	defer cancel()
	cmd, err := client.process(ctx, "app-server", "daemon", action)
	if err != nil {
		return "command-unavailable"
	}
	var output boundedOutput
	var diagnostic boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	err = cmd.Run()
	if err != nil {
		return fmt.Sprintf("exit=%d output=%s diagnostic=%s", processExitCode(err), classifyCodexDiagnostic(output.String()), classifyCodexDiagnostic(diagnostic.String()))
	}
	var status daemonStatus
	if json.Unmarshal(output.Bytes(), &status) != nil {
		return "invalid-status"
	}
	return "status=" + status.Status
}

func waitForCodexDaemon(t *testing.T, client codexClient) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := client.lifecycle("version")
		if err == nil && status.Status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated Codex daemon did not become ready: %s", probeCodexDaemon(client, "version"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func testRealCodexUsage(binary string) error {
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"synthetic local rejection"}}`))
	}))
	_, err := readProfileUsageWithCodex(context.Background(), realCodexSyntheticAuthForUsage(), binary, syntheticEndpointConfig(endpoint.URL))
	closeCodexTestServer(endpoint)
	if requests.Load() == 0 {
		return errors.New("Codex did not reach the controlled loopback endpoint")
	}
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "synthetic local rejection") || strings.Contains(strings.ToLower(err.Error()), "access_token") {
		return errors.New("Codex status failure was missing or exposed a protocol detail")
	}

	started := make(chan struct{}, 1)
	finished := make(chan struct{})
	var once sync.Once
	var finishOnce sync.Once
	blocked := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { started <- struct{}{} })
		<-r.Context().Done()
		finishOnce.Do(func() { close(finished) })
	}))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := readProfileUsageWithCodex(ctx, realCodexSyntheticAuthForUsage(), binary, syntheticEndpointConfig(blocked.URL))
		result <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(10 * time.Second):
		cancel()
		closeCodexTestServer(blocked)
		return errors.New("Codex did not start its controlled cancellation request")
	}
	select {
	case err := <-result:
		if err == nil {
			closeCodexTestServer(blocked)
			return errors.New("canceled Codex status request reported success")
		}
	case <-time.After(5 * time.Second):
		closeCodexTestServer(blocked)
		return errors.New("Codex status process did not stop after cancellation")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		closeCodexTestServer(blocked)
		return errors.New("controlled status connection remained open after cancellation")
	}
	closeCodexTestServer(blocked)
	return nil
}

func closeCodexTestServer(server *httptest.Server) {
	server.CloseClientConnections()
	server.Close()
}

func realCodexSyntheticAuthForUsage() []byte {
	payload, _ := json.Marshal(map[string]any{
		"sub": "synthetic-usage-user",
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	})
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
	raw, _ := json.Marshal(map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
		"tokens": map[string]string{
			"id_token": token, "access_token": token,
			"refresh_token": "synthetic-status-refresh", "account_id": "synthetic-status-account",
		},
	})
	return raw
}

func syntheticEndpointConfig(endpoint string) []byte {
	return []byte("cli_auth_credentials_store = \"file\"\ncheck_for_update_on_startup = false\nchatgpt_base_url = \"" + endpoint + "\"\nopenai_base_url = \"" + endpoint + "\"\n[analytics]\nenabled = false\n")
}

func bootstrapSyntheticDaemon(client codexClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd, err := client.process(ctx, "app-server", "daemon", "bootstrap")
	if err != nil {
		return errors.New("could not resolve the isolated Codex executable")
	}
	var output boundedOutput
	var diagnostic boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		combined := diagnostic.String() + "\n" + output.String()
		return fmt.Errorf("Codex daemon bootstrap failed (exit %d, diagnostic %s); ACLs: %s", processExitCode(err), classifyCodexDiagnostic(combined), codexIntegrationACLs(client.home))
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Status != "bootstrapped" {
		return errors.New("Codex daemon bootstrap returned an unexpected response")
	}
	return nil
}

func processExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func classifyCodexDiagnostic(text string) string {
	lower := strings.ToLower(text)
	var classes []string
	for _, marker := range []struct{ phrase, class string }{
		{"permission denied", "permission"},
		{"access is denied", "permission"},
		{"address family not supported", "af-unix-unsupported"},
		{"address already in use", "address-in-use"},
		{"filename or extension is too long", "path-too-long"},
		{"name too long", "path-too-long"},
		{"failed to create socket", "socket-create"},
		{"failed to create directory", "directory-create"},
		{"failed to create lock", "lock-create"},
		{"failed to create package", "package-create"},
		{"failed to create path", "path-create"},
		{"no such file or directory", "missing-path"},
		{"the system cannot find the path", "missing-path"},
		{"the system cannot find the file", "missing-file"},
		{"not supported", "unsupported"},
		{"no such file", "missing-file"},
		{"path alias", "path-alias"},
		{"helper", "helper"},
		{"temporary", "temporary"},
		{"temp dir", "temp-dir"},
		{"directory", "directory"},
		{"already exists", "already-exists"},
		{"invalid argument", "invalid-argument"},
		{"not found", "not-found"},
		{"failed to create", "create-failure"},
		{"socket", "socket"},
		{"network", "network"},
		{"auth", "authentication"},
		{"daemon package", "daemon-package"},
		{"detached", "detached-process"},
	} {
		if strings.Contains(lower, marker.phrase) {
			classes = append(classes, marker.class)
		}
	}
	if len(classes) > 0 {
		return strings.Join(classes, ",")
	}
	return "unclassified"
}

func newSyntheticCodexHome(t *testing.T) (string, error) {
	t.Helper()
	// Codex refuses to create helper aliases below the system temp directory.
	// Use a unique, disposable home directly under the current user's cache.
	basePath, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
	}
	home, err := os.MkdirTemp(base, "cs-")
	if err != nil {
		if runtime.GOOS != "windows" {
			return "", err
		}
		base, err = filepath.Abs(os.TempDir())
		if err != nil {
			return "", err
		}
		home, err = os.MkdirTemp(base, "cs-")
		if err != nil {
			return "", err
		}
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, absHome)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("synthetic Codex home escaped the test-owned temporary root")
	}
	// Codex 0.163.0 uses AF_UNIX on Windows, where the canonical path is limited
	// to 107 bytes plus its terminator. Fail instead of silently testing fallback.
	socket := filepath.Join(absHome, "app-server-control", "app-server-control.sock")
	if len([]byte(socket))+1 > 108 {
		_ = os.RemoveAll(absHome)
		return "", fmt.Errorf("temporary path is too long for the Windows Codex socket: %d bytes", len([]byte(socket))+1)
	}
	t.Cleanup(func() {
		current, err := filepath.Abs(absHome)
		if err != nil {
			t.Errorf("resolve synthetic Codex home for cleanup: %v", err)
			return
		}
		cleanRelative, err := filepath.Rel(base, current)
		if err != nil || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(os.PathSeparator)) {
			t.Errorf("refusing to remove synthetic Codex home outside its owned temp root")
			return
		}
		if err := os.RemoveAll(current); err != nil {
			t.Errorf("remove synthetic Codex home: %v", err)
		}
	})
	return absHome, nil
}

func realCodexSyntheticAuth(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"sub":   "synthetic-user-" + name,
		"email": "synthetic-" + name + "@example.invalid",
		"exp":   time.Now().Add(24 * time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_account_id": "synthetic-account-" + name,
			"chatgpt_user_id":    "synthetic-user-" + name,
			"chatgpt_plan_type":  "plus",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
	raw, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"id_token": token, "access_token": token,
			"refresh_token": "synthetic-refresh-" + name,
			"account_id":    "synthetic-account-" + name,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
