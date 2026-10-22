package switcher

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt in explicitly: this starts only a temporary daemon with synthetic credentials.
// All authentication and model endpoints point to an unused loopback port.
func TestRealCodexAccountRoundTrip(t *testing.T) {
	if os.Getenv("SWITCHER_REAL_CODEX") != "1" {
		t.Skip("set SWITCHER_REAL_CODEX=1 to test the installed Codex in a temporary home")
	}
	store := NewStore(t.TempDir())
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
	client := codexClient{home: store.codexHome}
	// Register cleanup before starting, including partial-start failure paths.
	t.Cleanup(func() {
		if _, err := client.lifecycle("stop"); err != nil {
			t.Error("stop isolated test daemon:", err)
		}
	})
	if _, err := client.lifecycle("start"); err != nil {
		t.Fatal("start isolated test daemon:", err)
	}
	// Reproduce the original bug: the selected file changes while the server
	// retains the previous account, even across a fresh switcher invocation.
	if _, err := store.Switch(profiles[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SwitchAccount(profiles[0].ID, false); !errors.Is(err, ErrServerRestartRequired) {
		t.Fatalf("stale real server was not detected: %v", err)
	}
	for _, profile := range []Profile{profiles[0], profiles[1], profiles[0]} {
		result, err := store.SwitchAccount(profile.ID, true)
		if err != nil || !result.ServerVerified {
			t.Fatalf("real Codex round trip: verified=%v err=%v", result.ServerVerified, err)
		}
	}
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
