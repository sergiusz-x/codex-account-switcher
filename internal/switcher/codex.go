package switcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"codex-account-switcher/internal/version"
	"github.com/coder/websocket"
)

var ErrServerRestartRequired = errors.New("switching requires restarting the shared Codex server")

type SwitchResult struct {
	Already        bool
	ServerVerified bool
}

// SwitchAccount coordinates a file switch with the managed Codex server.
// allowRestart must come from confirmation that other sessions may disconnect.
func (s *Store) SwitchAccount(profileID string, allowRestart bool) (SwitchResult, error) {
	return s.changeAccount(profileID, false, allowRestart, codexClient{home: s.codexHome})
}

func (s *Store) RestoreAccount(allowRestart bool) (SwitchResult, error) {
	return s.changeAccount("", true, allowRestart, codexClient{home: s.codexHome})
}

func (s *Store) changeAccount(profileID string, restore, allowRestart bool, client codexClient) (result SwitchResult, retErr error) {
	unlock, err := s.lock()
	if err != nil {
		return result, err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()

	return s.changeAccountLocked(profileID, restore, allowRestart, client, nil)
}

func (s *Store) changeAccountLocked(profileID string, restore, allowRestart bool, client codexClient, renewed []byte) (result SwitchResult, err error) {
	// Validate both identities before touching the running server or active file.
	data, err := s.loadState()
	if err != nil {
		return result, err
	}
	var target Profile
	var targetIdentity identity
	if restore {
		_, targetIdentity, err = readAuth(s.recoveryPath())
		if err != nil {
			return result, fmt.Errorf("%w: %v", ErrNoRecovery, err)
		}
		var ok bool
		target, ok = findIdentity(data, targetIdentity.key())
		if !ok {
			return result, ErrNoRecovery
		}
	} else {
		var ok bool
		target, ok = findProfile(data, profileID)
		if !ok {
			return result, ErrProfileNotFound
		}
		// Renewal was identity-checked when importing the isolated login. It can
		// repair an unavailable saved snapshot without trusting its old contents.
		if renewed == nil {
			_, targetIdentity, err = readAuth(s.profilePath(target.ID))
			if err != nil {
				return result, fmt.Errorf("selected login is unavailable: %w", err)
			}
			if targetIdentity.key() != target.Identity {
				return result, errors.New("selected login does not match its recorded identity")
			}
		}
	}
	_, currentIdentity, err := s.readCurrent()
	if err == nil {
		if _, ok := findIdentity(data, currentIdentity.key()); !ok {
			if restore {
				return result, ErrUnknownRecovery
			}
			return result, ErrUnknownCurrent
		}
	} else if !errors.Is(err, ErrNoAuth) && !(restore && errors.Is(err, ErrInvalidAuth)) {
		return result, err
	}

	status, err := client.lifecycle("version")
	if err != nil {
		return result, err
	}
	running := status.Status == "running"
	if running {
		serverToken, err := client.accountToken(status.SocketPath)
		if err != nil {
			return result, fmt.Errorf("read running Codex account; login was not changed: %w", err)
		}
		currentBytes, latestIdentity, err := s.readCurrent()
		if err != nil && !errors.Is(err, ErrNoAuth) && !(restore && errors.Is(err, ErrInvalidAuth)) {
			return result, err
		}
		if renewed == nil && !restore && latestIdentity.key() == target.Identity && serverToken != "" && serverToken == accessToken(currentBytes) {
			result.Already, err = s.switchProfile(target.ID)
			result.ServerVerified = err == nil
			return result, err
		}
		if status.Backend == "" {
			return result, errors.New("Codex server is not managed by the CLI; close its owning app or use codex --no-daemon; login was not changed")
		}
		if !allowRestart {
			return result, ErrServerRestartRequired
		}
		if _, err := client.lifecycle("stop"); err != nil {
			return result, fmt.Errorf("could not stop Codex; login was not changed: %w", err)
		}
		// The CLI's stopped/notRunning acknowledgment confirms shutdown. A fresh
		// version probe can fail on the stale control socket left by Codex 0.163.0.
	}

	// Re-read after shutdown so any final refreshed tokens are preserved.
	if renewed != nil {
		err = s.installRenewed(data, target, renewed)
	} else if restore {
		err = s.restoreProfile()
	} else {
		result.Already, err = s.switchProfile(target.ID)
	}
	if err != nil {
		if running {
			_, restartErr := client.lifecycle("start")
			if restartErr != nil {
				err = errors.Join(err, fmt.Errorf("could not restart Codex after failed switch: %w", restartErr))
			}
		}
		return result, err
	}
	if !running {
		return result, nil
	}
	result.Already = false // The running server needed to change, even if the file already matched.
	started, err := client.lifecycle("start")
	if err != nil {
		return result, fmt.Errorf("login file selected, but Codex did not start: %w", err)
	}
	serverToken, err := client.accountToken(started.SocketPath)
	if err != nil {
		return result, fmt.Errorf("login file selected, but the Codex account could not be verified: %w", err)
	}
	if serverToken == "" {
		return result, errors.New("Codex reports no login after loading the selected profile; its saved credentials may need renewal; no automatic login or logout was attempted")
	}
	liveBytes, liveIdentity, err := s.readCurrent()
	if err != nil || liveIdentity.key() != target.Identity {
		return result, errors.New("Codex account and auth.json no longer agree; switch was not confirmed")
	}
	if serverToken != accessToken(liveBytes) {
		return result, errors.New("Codex still uses different credentials; check its credential storage and configuration; switch was not confirmed")
	}
	if _, err := s.syncCurrent(); err != nil {
		return result, fmt.Errorf("Codex loaded the selected account, but its updated login could not be saved: %w", err)
	}
	result.ServerVerified = true
	return result, nil
}

type codexClient struct {
	home    string
	timeout time.Duration
	command func(context.Context, ...string) *exec.Cmd
}

type daemonStatus struct {
	Status     string `json:"status"`
	Backend    string `json:"backend"`
	SocketPath string `json:"socketPath"`
}

func (c codexClient) process(ctx context.Context, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if c.command != nil {
		cmd = c.command(ctx, args...)
	} else {
		cmd = exec.CommandContext(ctx, "codex", args...)
	}
	cmd.Env = append(cmd.Environ(), "CODEX_HOME="+c.home)
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Stderr = io.Discard // Never include upstream output, which may contain secrets.
	return cmd
}

func (c codexClient) deadline() (context.Context, context.CancelFunc) {
	d := c.timeout
	if d == 0 {
		d = 20 * time.Second
	}
	return context.WithTimeout(context.Background(), d)
}

func (c codexClient) lifecycle(action string) (daemonStatus, error) {
	ctx, cancel := c.deadline()
	defer cancel()
	cmd := c.process(ctx, "app-server", "daemon", action)
	var output boundedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return daemonStatus{}, fmt.Errorf("Codex server %s timed out; its state is uncertain", action)
		}
		return daemonStatus{}, fmt.Errorf("Codex server %s failed; use a supported Codex CLI with file-based login", action)
	}
	var status daemonStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		return status, errors.New("Codex returned an invalid server status")
	}
	valid := (action == "version" && (status.Status == "running" || status.Status == "notRunning")) ||
		(action == "stop" && (status.Status == "stopped" || status.Status == "notRunning")) ||
		(action == "start" && (status.Status == "started" || status.Status == "alreadyRunning"))
	if !valid {
		return status, errors.New("Codex returned an unexpected server status")
	}
	return status, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > maxAuthSize-b.Len() {
		return 0, errors.New("Codex response exceeds the size limit")
	}
	return b.Buffer.Write(p)
}

// accountToken reads the existing server over its local WebSocket connection.
// The legacy getAuthStatus method avoids account/read's network routing discovery.
// Its secret response is used only for comparison and must never reach the UI.
func (c codexClient) accountToken(socket string) (token string, retErr error) {
	if !filepath.IsAbs(socket) {
		return "", errors.New("Codex did not report an absolute local socket path")
	}
	ctx, cancel := c.deadline()
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	conn, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		return "", errors.New("cannot connect to the local Codex server")
	}
	conn.SetReadLimit(maxAuthSize)
	defer func() {
		if err := conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
			retErr = errors.Join(retErr, errors.New("could not close the Codex connection cleanly"))
		}
	}()
	write := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return errors.New("cannot encode a Codex request")
		}
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			return errors.New("cannot send a Codex request")
		}
		return nil
	}
	request := func(id int, method string, params any, target any) error {
		if err := write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return fmt.Errorf("Codex %s connection closed or timed out", method)
			}
			var response struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(raw, &response) != nil {
				return errors.New("Codex returned an invalid response")
			}
			if response.ID == nil || *response.ID != id {
				continue
			}
			if len(response.Error) != 0 && string(response.Error) != "null" {
				return errors.New("Codex rejected the account verification request")
			}
			if json.Unmarshal(response.Result, target) != nil {
				return errors.New("Codex returned an unsupported response")
			}
			return nil
		}
	}
	var initialized struct {
		CodexHome string `json:"codexHome"`
	}
	if err := request(1, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "codex_account_switcher", "version": version.Version()},
	}, &initialized); err != nil {
		return "", err
	}
	wantHome, wantErr := os.Stat(c.home)
	gotHome, gotErr := os.Stat(initialized.CodexHome)
	if wantErr != nil || gotErr != nil || !os.SameFile(wantHome, gotHome) {
		return "", errors.New("Codex server uses a different home directory")
	}
	if err := write(map[string]string{"method": "initialized"}); err != nil {
		return "", err
	}
	var auth struct {
		Method string `json:"authMethod"`
		Token  string `json:"authToken"`
	}
	if err := request(2, "getAuthStatus", map[string]bool{"refreshToken": false, "includeToken": true}, &auth); err != nil {
		return "", err
	}
	if auth.Method == "" {
		return "", nil
	}
	if auth.Method != "chatgpt" || auth.Token == "" {
		return "", errors.New("Codex did not expose verifiable ChatGPT credentials; use a compatible CLI with file-based login")
	}
	return auth.Token, nil
}

func accessToken(raw []byte) string {
	var auth authFile
	if json.Unmarshal(raw, &auth) != nil {
		return ""
	}
	return auth.Tokens.AccessToken
}
