package switcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Login owns an empty, private Codex home for one interactive sign-in.
// Complete must be called after Command exits, including when execution fails.
type Login struct {
	Command *exec.Cmd
	home    string
	cancel  context.CancelFunc
}

func NewLogin() (*Login, error) {
	home, err := os.MkdirTemp("", "codex-switch-login-*")
	if err != nil {
		return nil, errors.New("cannot prepare a private login directory")
	}
	if err := secureDataDir(home); err != nil {
		return nil, errors.Join(errors.New("cannot protect the private login directory"), err, os.RemoveAll(home))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	cmd, err := newCodexCommand(ctx, "login", "--device-auth", "-c", `cli_auth_credentials_store="file"`)
	if err != nil {
		cancel()
		return nil, errors.Join(err, os.RemoveAll(home))
	}
	cmd.Dir = home // Do not load a repository's project configuration.
	cmd.Env = loginEnvironment(os.Environ(), home)
	cmd.WaitDelay = time.Second
	return &Login{Command: cmd, home: home, cancel: cancel}, nil
}

func loginEnvironment(environment []string, home string) []string {
	var env []string
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if environmentKeyEqual(key, "CODEX_HOME") || environmentKeyEqual(key, "OPENAI_API_KEY") || environmentKeyEqual(key, "CODEX_API_KEY") || environmentKeyEqual(key, "CODEX_ACCESS_TOKEN") {
			continue
		}
		env = append(env, entry)
	}
	return setEnvironmentValue(env, "CODEX_HOME", home)
}

// Complete imports only a successful, identity-checked login. An empty profileID
// adds an account; otherwise the caller confirmed renewal and a possible restart.
func (l *Login) Complete(s *Store, name, profileID string, processErr error) (result SwitchResult, retErr error) {
	defer func() {
		l.cancel()
		if err := os.RemoveAll(l.home); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("could not remove private login directory %s; it may still contain credentials", l.home))
		}
	}()
	if processErr != nil {
		return result, errors.New("login was canceled, timed out, or failed; saved profiles were not changed")
	}
	raw, id, err := readAuth(filepath.Join(l.home, "auth.json"))
	if err != nil {
		return result, errors.New("Codex did not save a complete file-based ChatGPT login; saved profiles were not changed")
	}
	return s.importLogin(name, profileID, raw, id)
}

func (s *Store) importLogin(name, profileID string, raw []byte, id identity) (result SwitchResult, retErr error) {
	name = strings.TrimSpace(name)
	if profileID == "" {
		if err := validateName(name); err != nil {
			return result, err
		}
	}
	if err := os.MkdirAll(s.codexHome, 0o700); err != nil {
		return result, errors.New("cannot create the Codex home directory")
	}
	unlock, err := s.lock()
	if err != nil {
		return result, err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()
	data, err := s.loadState()
	if err != nil {
		return result, err
	}
	if profileID != "" {
		profile, found := findProfile(data, profileID)
		if !found {
			return result, ErrProfileNotFound
		}
		if profile.Identity != id.key() {
			return result, errors.New("signed in to a different account; renewal canceled and saved profiles left unchanged")
		}
		_, current, err := s.readCurrent()
		if err != nil && !errors.Is(err, ErrNoAuth) {
			return result, err
		}
		if current.key() == profile.Identity {
			return s.changeAccountLocked(profileID, false, true, codexClient{home: s.codexHome}, raw)
		}
		// A recovery snapshot for this account must not keep the old login.
		if _, recovery, err := readAuth(s.recoveryPath()); err == nil && recovery.key() == profile.Identity {
			if err := atomicWrite(s.recoveryPath(), raw, 0o600); err != nil {
				return result, fmt.Errorf("update matching recovery login: %w", err)
			}
		}
		return result, s.saveImported(data, profile, raw)
	}
	_, current, currentErr := s.readCurrent()
	if currentErr != nil && !errors.Is(currentErr, ErrNoAuth) {
		return result, currentErr
	}
	if current.key() == id.key() {
		return result, errors.New("this account is already the current login; save it first, then use More > Renew login")
	}
	for _, profile := range data.Profiles {
		if profile.Identity == id.key() {
			return result, errors.New("this account is already saved; use More > Renew login on its profile")
		}
		if profile.Name == name {
			return result, ErrProfileNameTaken
		}
	}
	profileID, err = newID()
	if err != nil {
		return result, err
	}
	profile := Profile{ID: profileID, Name: name, Identity: id.key()}
	data.Profiles = append(data.Profiles, profile)
	return result, s.saveImported(data, profile, raw)
}

func (s *Store) saveImported(data state, profile Profile, raw []byte) error {
	if err := atomicWrite(s.profilePath(profile.ID), raw, 0o600); err != nil {
		return fmt.Errorf("save new login: %w", err)
	}
	data.markSaved(profile.ID)
	if err := s.saveState(data); err != nil {
		return fmt.Errorf("new credentials were saved, but the profile index could not be updated: %w", err)
	}
	return nil
}

// installRenewed runs after server shutdown, under the switcher lock. Updating
// the live file first prevents synchronization from restoring the old snapshot.
func (s *Store) installRenewed(data state, profile Profile, raw []byte) error {
	current, id, err := s.readCurrent()
	if err != nil {
		return err
	}
	if id.key() != profile.Identity {
		return ErrCurrentChanged
	}
	if err := atomicWrite(s.recoveryPath(), current, 0o600); err != nil {
		return fmt.Errorf("preserve login before renewal: %w", err)
	}
	data.ActiveID = profile.ID
	if _, err := s.installTarget(data, profile, raw, current); err != nil {
		return err
	}
	if err := s.saveImported(data, profile, raw); err != nil {
		return fmt.Errorf("active login renewed, but its saved profile needs synchronization: %w", err)
	}
	return nil
}
