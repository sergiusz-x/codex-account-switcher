package switcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeDaemon struct {
	Running    bool
	Account    string
	Token      string
	Backend    string
	Fail       string
	Renew      bool
	WrongHome  bool
	WrongID    bool
	LoggedOut  bool
	SocketPath string
}

func fakeCodexClient(t *testing.T, home string, state fakeDaemon) codexClient {
	t.Helper()
	if state.Account == "account-a" {
		state.Token = "access-a"
	} else if state.Account == "account-b" {
		state.Token = "access-b"
	}
	directory, err := os.MkdirTemp("/tmp", "cs-ws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	state.SocketPath = filepath.Join(directory, "server.sock")
	listener, err := net.Listen("unix", state.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFakeCodex(w, r, home)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	writeFakeDaemon(t, home, state)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return codexClient{home: home, timeout: 2 * time.Second, command: func(ctx context.Context, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, binary, append([]string{"-test.run=^TestCodexProcessHelper$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "SWITCHER_CODEX_HELPER=1")
		return cmd
	}}
}

func writeFakeDaemon(t *testing.T, home string, state fakeDaemon) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, filepath.Join(home, "fake-daemon.json"), raw)
}

func twoProfiles(t *testing.T) (*Store, Profile, Profile) {
	t.Helper()
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-b", "subject-b", "access-b", "future-b"))
	b, err := store.SaveCurrent("B", false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "access-a", "future-a"))
	a, err := store.SaveCurrent("A", false)
	if err != nil {
		t.Fatal(err)
	}
	return store, a, b
}

func TestAccountSwitchRoundTripRestartsAndVerifiesServer(t *testing.T) {
	store, a, b := twoProfiles(t)
	client := fakeCodexClient(t, store.codexHome, fakeDaemon{Running: true, Account: "account-a", Backend: "pid", Renew: true})
	original := readTestFile(t, store.authPath())
	if _, err := store.changeAccount(b.ID, false, false, client); !errors.Is(err, ErrServerRestartRequired) {
		t.Fatalf("switch without confirmation: %v", err)
	}
	if string(readTestFile(t, store.authPath())) != string(original) {
		t.Fatal("confirmation prompt changed the login")
	}
	for _, target := range []Profile{b, a} {
		result, err := store.changeAccount(target.ID, false, true, client)
		if err != nil || !result.ServerVerified {
			t.Fatalf("switch to %s: result=%+v err=%v", target.Name, result, err)
		}
		live, got, err := readAuth(store.authPath())
		if err != nil || got.key() != target.Identity {
			t.Fatal("server switch installed the wrong account")
		}
		if string(readTestFile(t, store.profilePath(target.ID))) != string(live) {
			t.Fatal("server startup refreshed tokens but the saved profile was not updated")
		}
	}
	_, preserved, err := readAuth(store.profilePath(b.ID))
	if err != nil || preserved.key() != b.Identity {
		t.Fatal("outgoing account was saved to the wrong profile")
	}
	var saved authFile
	if err := json.Unmarshal(readTestFile(t, store.profilePath(b.ID)), &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Tokens.RefreshToken, "stop") {
		t.Fatal("final token refresh during shutdown was lost")
	}
	if !strings.Contains(string(readTestFile(t, store.profilePath(a.ID))), "future-a") {
		t.Fatal("switch lost unknown auth fields")
	}
}

func TestCurrentFileWithStaleDaemonStillRequiresRestart(t *testing.T) {
	store, a, _ := twoProfiles(t)
	client := fakeCodexClient(t, store.codexHome, fakeDaemon{Running: true, Account: "account-b", Backend: "pid"})
	if _, err := store.changeAccount(a.ID, false, false, client); !errors.Is(err, ErrServerRestartRequired) {
		t.Fatalf("current file hid stale daemon: %v", err)
	}
	result, err := store.changeAccount(a.ID, false, true, client)
	if err != nil || !result.ServerVerified {
		t.Fatalf("reload selected file: result=%+v err=%v", result, err)
	}
}

func TestAccountSwitchFailuresNeverReportSuccess(t *testing.T) {
	for _, scenario := range []string{"stop", "start", "malformed", "rpc", "wrong account", "logged out", "wrong home", "unmanaged", "invalid target"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, b := twoProfiles(t)
			state := fakeDaemon{Running: true, Account: "account-a", Backend: "pid"}
			switch scenario {
			case "stop", "start", "malformed", "rpc":
				state.Fail = scenario
			case "wrong account":
				state.WrongID = true
			case "logged out":
				state.LoggedOut = true
			case "wrong home":
				state.WrongHome = true
			case "unmanaged":
				state.Backend = ""
			case "invalid target":
				writeTestAuth(t, store.profilePath(b.ID), fakeAuth("unrelated", "other-user", "other", ""))
			}
			client := fakeCodexClient(t, store.codexHome, state)
			original := readTestFile(t, store.authPath())
			result, err := store.changeAccount(b.ID, false, true, client)
			if err == nil || result.ServerVerified {
				t.Fatalf("failure reported success: result=%+v err=%v", result, err)
			}
			if strings.Contains(err.Error(), "SECRET_UPSTREAM_OUTPUT") {
				t.Fatal("upstream output leaked into an error")
			}
			if scenario == "stop" || scenario == "malformed" || scenario == "rpc" || scenario == "wrong home" || scenario == "unmanaged" || scenario == "invalid target" {
				if string(readTestFile(t, store.authPath())) != string(original) {
					t.Fatal("pre-switch failure changed the active login")
				}
			}
		})
	}
}

func TestAlreadyLoadedAccountDoesNotRestartServer(t *testing.T) {
	store, a, _ := twoProfiles(t)
	client := fakeCodexClient(t, store.codexHome, fakeDaemon{Running: true, Account: "account-a", Backend: "pid", Fail: "stop"})
	result, err := store.changeAccount(a.ID, false, false, client)
	if err != nil || !result.Already || !result.ServerVerified {
		t.Fatalf("already loaded account: result=%+v err=%v", result, err)
	}
}

func TestServerTimeoutDoesNotChangeAuth(t *testing.T) {
	store, _, b := twoProfiles(t)
	client := fakeCodexClient(t, store.codexHome, fakeDaemon{Fail: "timeout"})
	client.timeout = 100 * time.Millisecond
	original := readTestFile(t, store.authPath())
	if _, err := store.changeAccount(b.ID, false, true, client); err == nil {
		t.Fatal("hung daemon command did not time out")
	}
	if string(readTestFile(t, store.authPath())) != string(original) {
		t.Fatal("timed-out switch changed the active login")
	}
}

func TestStoppedServerIsNotStartedByFileSwitch(t *testing.T) {
	store, _, b := twoProfiles(t)
	client := fakeCodexClient(t, store.codexHome, fakeDaemon{})
	result, err := store.changeAccount(b.ID, false, false, client)
	if err != nil || result.ServerVerified {
		t.Fatalf("file selection: result=%+v err=%v", result, err)
	}
	var state fakeDaemon
	if err := json.Unmarshal(readTestFile(t, filepath.Join(store.codexHome, "fake-daemon.json")), &state); err != nil || state.Running {
		t.Fatal("file selection started the server")
	}
}

func TestRestoreAlsoReloadsServer(t *testing.T) {
	store, a, b := twoProfiles(t)
	if _, err := store.Switch(b.ID); err != nil {
		t.Fatal(err)
	}
	client := fakeCodexClient(t, store.codexHome, fakeDaemon{Running: true, Account: "account-b", Backend: "pid"})
	result, err := store.changeAccount("", true, true, client)
	if err != nil || !result.ServerVerified {
		t.Fatalf("restore: result=%+v err=%v", result, err)
	}
	_, id, err := readAuth(store.authPath())
	if err != nil || id.key() != a.Identity {
		t.Fatal("restore loaded the wrong account")
	}
}

// This subprocess implements the Codex CLI boundary using synthetic local files.
func TestCodexProcessHelper(t *testing.T) {
	if os.Getenv("SWITCHER_CODEX_HELPER") != "1" {
		return
	}
	home := os.Getenv("CODEX_HOME")
	statePath := filepath.Join(home, "fake-daemon.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		os.Exit(2)
	}
	var state fakeDaemon
	if json.Unmarshal(raw, &state) != nil {
		os.Exit(2)
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 3 || args[1] != "app-server" {
		os.Exit(2)
	}
	args = args[2:]
	encode := func(v any) {
		if json.NewEncoder(os.Stdout).Encode(v) != nil {
			os.Exit(2)
		}
	}
	if args[0] == "daemon" {
		action := args[1]
		if state.Fail == "timeout" {
			time.Sleep(5 * time.Second)
			os.Exit(2)
		}
		if state.Fail == action {
			fmt.Fprintln(os.Stderr, "SECRET_UPSTREAM_OUTPUT")
			os.Exit(2)
		}
		if state.Fail == "malformed" {
			fmt.Println("SECRET_UPSTREAM_OUTPUT")
			os.Exit(0)
		}
		status := "notRunning"
		if state.Running {
			status = "running"
		}
		if action == "stop" || action == "start" {
			if state.Renew {
				var auth map[string]any
				raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
				if err != nil || json.Unmarshal(raw, &auth) != nil {
					os.Exit(2)
				}
				tokens := auth["tokens"].(map[string]any)
				tokens["access_token"] = tokens["access_token"].(string) + "-" + action
				tokens["refresh_token"] = tokens["refresh_token"].(string) + "-" + action
				raw, err = json.Marshal(auth)
				if err != nil || os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600) != nil {
					os.Exit(2)
				}
			}
			state.Running = action == "start"
			status = "stopped"
			if state.Running {
				status = "started"
				_, id, err := readAuth(filepath.Join(home, "auth.json"))
				if err != nil {
					os.Exit(2)
				}
				state.Account = id.AccountID
				current, err := os.ReadFile(filepath.Join(home, "auth.json"))
				if err != nil {
					os.Exit(2)
				}
				state.Token = accessToken(current)
				if state.WrongID {
					state.Account = "unexpected-account"
					state.Token = "unexpected-token"
				}
				if state.LoggedOut {
					state.Account = ""
					state.Token = ""
				}
			}
			raw, err = json.Marshal(state)
			if err != nil || os.WriteFile(statePath, raw, 0o600) != nil {
				os.Exit(2)
			}
		}
		encode(daemonStatus{Status: status, Backend: state.Backend, SocketPath: state.SocketPath})
		os.Exit(0)
	}
	os.Exit(2)
}

func serveFakeCodex(w http.ResponseWriter, r *http.Request, home string) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := context.Background()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var request struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(raw, &request) != nil {
			return
		}
		raw, err = os.ReadFile(filepath.Join(home, "fake-daemon.json"))
		var state fakeDaemon
		if err != nil || json.Unmarshal(raw, &state) != nil || !state.Running {
			return
		}
		if state.Fail == "rpc" {
			raw, err := json.Marshal(map[string]any{"id": request.ID, "error": map[string]string{"message": "SECRET_UPSTREAM_OUTPUT"}})
			if err == nil {
				_ = conn.Write(ctx, websocket.MessageText, raw)
			}
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			serverHome := home
			if state.WrongHome {
				serverHome = filepath.Dir(home)
			}
			result = map[string]string{"codexHome": serverHome}
		case "initialized":
			continue
		case "getAuthStatus":
			if request.Params["refreshToken"] != false || request.Params["includeToken"] != true {
				return
			}
			auth := map[string]any{"authMethod": nil, "authToken": nil}
			if state.Account != "" {
				auth["authMethod"] = "chatgpt"
				auth["authToken"] = state.Token
			}
			result = auth
		default:
			return
		}
		raw, err = json.Marshal(map[string]any{"id": request.ID, "result": result})
		if err != nil || conn.Write(ctx, websocket.MessageText, raw) != nil {
			return
		}
	}
}
