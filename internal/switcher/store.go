package switcher

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const maxAuthSize = 1 << 20

var (
	ErrNoAuth           = errors.New("auth.json does not exist")
	ErrUnknownCurrent   = errors.New("the current account is not saved as a profile")
	ErrProfileExists    = errors.New("a profile for this account already exists")
	ErrProfileNotFound  = errors.New("profile was not found")
	ErrProfileNameTaken = errors.New("a profile with this name already exists")
	ErrProfileNameDiff  = errors.New("this account is saved under a different profile name")
	ErrNameTaken        = errors.New("this name belongs to a different account")
	ErrCurrentChanged   = errors.New("auth.json changed during the operation; refresh and try again")
	ErrNoRecovery       = errors.New("no valid recovery copy is available")
	ErrUnknownRecovery  = errors.New("the current account is not saved; save it before restoring")
	ErrInvalidAuth      = errors.New("auth.json is invalid")
)

type Profile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	SavedAt  string `json:"saved_at"`
}

type state struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
	ActiveID string    `json:"active_id,omitempty"`
}

type authFile struct {
	Mode   string `json:"auth_mode"`
	Tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

type identity struct {
	AccountID string `json:"account_id"`
	Subject   string `json:"subject"`
}

type Store struct {
	codexHome string
	root      string
}

func NewStore(codexHome string) *Store {
	root := filepath.Join(codexHome, "switcher")
	return &Store{codexHome: codexHome, root: root}
}

func (s *Store) List() ([]Profile, error) {
	data, err := s.loadState()
	if err != nil {
		return nil, err
	}
	return append([]Profile(nil), data.Profiles...), nil
}

func (s *Store) ActiveID() (string, bool, error) {
	data, err := s.loadState()
	if err != nil {
		return "", false, err
	}
	return s.currentProfile(data)
}

func (s *Store) HasRecovery() (bool, error) {
	_, _, err := readAuth(s.recoveryPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SyncCurrent updates only the saved profile matching the current login.
// It preserves the full auth file and never replaces the active auth.json.
func (s *Store) SyncCurrent() (updated bool, retErr error) {
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()
	return s.syncCurrent()
}

func (s *Store) syncCurrent() (bool, error) {
	data, err := s.loadState()
	if err != nil {
		return false, err
	}
	raw, currentIdentity, err := s.readCurrent()
	if errors.Is(err, ErrNoAuth) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for i := range data.Profiles {
		profile := &data.Profiles[i]
		if profile.Identity != currentIdentity.key() {
			continue
		}
		saved, savedIdentity, err := readAuth(s.profilePath(profile.ID))
		if err != nil {
			return false, fmt.Errorf("read saved profile for synchronization: %w", err)
		}
		if savedIdentity.key() != profile.Identity {
			return false, errors.New("saved profile does not match its recorded identity; refusing to update")
		}
		if bytes.Equal(saved, raw) {
			return false, nil
		}
		if err := atomicWrite(s.profilePath(profile.ID), raw, 0o600); err != nil {
			return false, fmt.Errorf("update saved login: %w", err)
		}
		profile.SavedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveState(data); err != nil {
			return true, fmt.Errorf("saved login was updated, but the index could not be saved: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// SaveCurrent saves the exact current auth.json bytes. replace permits updating
// an existing profile only when its identity matches the currently logged-in account.
func (s *Store) SaveCurrent(name string, replace bool) (result Profile, retErr error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Profile{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return Profile{}, err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()

	data, err := s.loadState()
	if err != nil {
		return Profile{}, err
	}
	raw, currentIdentity, err := s.readCurrent()
	if err != nil {
		return Profile{}, err
	}

	key := currentIdentity.key()
	for i := range data.Profiles {
		existing := &data.Profiles[i]
		if existing.Name != name {
			continue
		}
		if existing.Identity != key {
			return Profile{}, ErrNameTaken
		}
		if !replace {
			return Profile{}, ErrProfileExists
		}
		if err := atomicWrite(s.profilePath(existing.ID), raw, 0o600); err != nil {
			return Profile{}, fmt.Errorf("write profile: %w", err)
		}
		existing.SavedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveState(data); err != nil {
			return Profile{}, fmt.Errorf("profile was saved, but the index could not be saved: %w", err)
		}
		return *existing, nil
	}

	for _, existing := range data.Profiles {
		if existing.Identity == key {
			if existing.Name == name {
				return Profile{}, ErrProfileExists
			}
			return Profile{}, ErrProfileNameDiff
		}
	}

	id, err := newID()
	if err != nil {
		return Profile{}, err
	}
	profile := Profile{ID: id, Name: name, Identity: key, SavedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := atomicWrite(s.profilePath(id), raw, 0o600); err != nil {
		return Profile{}, fmt.Errorf("write profile: %w", err)
	}
	data.Profiles = append(data.Profiles, profile)
	if err := s.saveState(data); err != nil {
		return Profile{}, fmt.Errorf("profile was saved, but the index could not be saved: %w", err)
	}
	return profile, nil
}

func (s *Store) RenameProfile(id, name string) (retErr error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()

	data, err := s.loadState()
	if err != nil {
		return err
	}
	profile, found := findProfile(data, id)
	if !found {
		return ErrProfileNotFound
	}
	if profile.Name == name {
		return nil
	}
	for _, existing := range data.Profiles {
		if existing.ID != id && existing.Name == name {
			return ErrProfileNameTaken
		}
	}
	for i := range data.Profiles {
		if data.Profiles[i].ID == id {
			data.Profiles[i].Name = name
			break
		}
	}
	if err := s.saveState(data); err != nil {
		return fmt.Errorf("save renamed profile: %w", err)
	}
	return nil
}

func (s *Store) DeleteProfile(id string) (retErr error) {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()

	data, err := s.loadState()
	if err != nil {
		return err
	}
	index := -1
	for i, profile := range data.Profiles {
		if profile.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrProfileNotFound
	}
	profile := data.Profiles[index]
	profilePath := s.profilePath(profile.ID)
	profileBytes, err := readRegularFile(profilePath, maxAuthSize)
	if err != nil {
		return fmt.Errorf("read profile before deleting: %w", err)
	}

	var recoveryBytes []byte
	removeRecovery := false
	if raw, recoveryIdentity, err := readAuth(s.recoveryPath()); err == nil && recoveryIdentity.key() == profile.Identity {
		recoveryBytes = raw
		removeRecovery = true
	}

	updated := data
	updated.Profiles = make([]Profile, 0, len(data.Profiles)-1)
	updated.Profiles = append(updated.Profiles, data.Profiles[:index]...)
	updated.Profiles = append(updated.Profiles, data.Profiles[index+1:]...)
	if updated.ActiveID == id {
		updated.ActiveID = ""
	}
	if err := s.saveState(updated); err != nil {
		return fmt.Errorf("update profile index before deleting: %w", err)
	}

	profileRemoved := false
	recoveryRemoved := false
	rollback := func(cause error) error {
		var rollbackErrs []error
		rollbackErrs = append(rollbackErrs, cause)
		if profileRemoved {
			if err := atomicWrite(profilePath, profileBytes, 0o600); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restore profile file: %w", err))
			}
		}
		if recoveryRemoved {
			if err := atomicWrite(s.recoveryPath(), recoveryBytes, 0o600); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restore recovery copy: %w", err))
			}
		}
		if len(rollbackErrs) == 1 {
			if err := s.saveState(data); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restore profile index: %w", err))
			}
		}
		return errors.Join(rollbackErrs...)
	}

	if err := os.Remove(profilePath); err != nil {
		return rollback(fmt.Errorf("remove profile file: %w", err))
	}
	profileRemoved = true
	if err := syncDir(filepath.Dir(profilePath)); err != nil {
		return rollback(fmt.Errorf("sync profiles directory: %w", err))
	}
	if removeRecovery {
		if err := os.Remove(s.recoveryPath()); err != nil {
			return rollback(fmt.Errorf("remove matching recovery copy: %w", err))
		}
		recoveryRemoved = true
		if err := syncDir(filepath.Dir(s.recoveryPath())); err != nil {
			return rollback(fmt.Errorf("sync recovery directory: %w", err))
		}
	}
	return nil
}

func (s *Store) Switch(profileID string) (already bool, retErr error) {
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()
	return s.switchProfile(profileID)
}

func (s *Store) switchProfile(profileID string) (bool, error) {
	data, err := s.loadState()
	if err != nil {
		return false, err
	}
	target, ok := findProfile(data, profileID)
	if !ok {
		return false, errors.New("selected profile was not found")
	}
	targetBytes, targetIdentity, err := readAuth(s.profilePath(target.ID))
	if err != nil {
		return false, fmt.Errorf("profile %q is unavailable or invalid: %w", target.Name, err)
	}
	if targetIdentity.key() != target.Identity {
		return false, fmt.Errorf("profile %q does not match its recorded identity; refusing to switch", target.Name)
	}

	currentBytes, currentIdentity, err := s.readCurrent()
	if errors.Is(err, ErrNoAuth) {
		if err := atomicWrite(s.authPath(), targetBytes, 0o600); err != nil {
			return false, fmt.Errorf("install profile: %w", err)
		}
		return s.finishSwitch(data, target, targetBytes)
	}
	if err != nil {
		return false, err
	}

	currentProfile, recognized := findIdentity(data, currentIdentity.key())
	if !recognized {
		return false, ErrUnknownCurrent
	}
	if currentProfile.ID == target.ID {
		if !bytes.Equal(currentBytes, targetBytes) {
			if err := atomicWrite(s.profilePath(target.ID), currentBytes, 0o600); err != nil {
				return false, fmt.Errorf("the refreshed login is active, but its current copy could not be saved: %w", err)
			}
			data.markSaved(target.ID)
			if err := s.saveState(data); err != nil {
				return false, fmt.Errorf("the current login was saved, but the index could not be saved: %w", err)
			}
		}
		return true, nil
	}

	if err := atomicWrite(s.profilePath(currentProfile.ID), currentBytes, 0o600); err != nil {
		return false, fmt.Errorf("preserve current profile %q: %w", currentProfile.Name, err)
	}
	data.markSaved(currentProfile.ID)
	if err := atomicWrite(s.recoveryPath(), currentBytes, 0o600); err != nil {
		return false, fmt.Errorf("current profile was saved, but recovery copy could not be prepared: %w", err)
	}
	return s.installTarget(data, target, targetBytes, currentBytes)
}

func (s *Store) Restore() (retErr error) {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()
	return s.restoreProfile()
}

func (s *Store) restoreProfile() error {
	data, err := s.loadState()
	if err != nil {
		return err
	}
	recoveryBytes, recoveryIdentity, err := readAuth(s.recoveryPath())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoRecovery, err)
	}
	recoveryProfile, ok := findIdentity(data, recoveryIdentity.key())
	if !ok {
		return fmt.Errorf("%w: recovery account is not saved as a profile", ErrNoRecovery)
	}

	currentBytes, currentIdentity, err := s.readCurrent()
	if err == nil {
		profile, ok := findIdentity(data, currentIdentity.key())
		if !ok {
			return ErrUnknownRecovery
		}
		if profile.Identity == recoveryIdentity.key() {
			return errors.New("the recovery copy already matches the active account")
		}
		if err := atomicWrite(s.profilePath(profile.ID), currentBytes, 0o600); err != nil {
			return fmt.Errorf("preserve current profile: %w", err)
		}
		data.markSaved(profile.ID)
		if err := s.saveState(data); err != nil {
			return fmt.Errorf("current profile was saved, but the index could not be saved: %w", err)
		}
		if err := atomicWrite(s.recoveryPath(), currentBytes, 0o600); err != nil {
			return fmt.Errorf("current profile was saved, but the recovery copy could not be refreshed: %w", err)
		}
	} else if errors.Is(err, ErrInvalidAuth) {
		corruptBytes, readErr := readRegularFile(s.authPath(), maxAuthSize)
		if readErr != nil {
			return fmt.Errorf("preserve invalid auth.json before recovery: %w", readErr)
		}
		id, idErr := newID()
		if idErr != nil {
			return idErr
		}
		corruptPath := filepath.Join(s.root, "recovery", "invalid-current-"+id+".json")
		if writeErr := atomicWrite(corruptPath, corruptBytes, 0o600); writeErr != nil {
			return fmt.Errorf("preserve invalid auth.json before recovery: %w", writeErr)
		}
	} else if !errors.Is(err, ErrNoAuth) {
		return err
	}

	if err := atomicWrite(s.authPath(), recoveryBytes, 0o600); err != nil {
		return fmt.Errorf("restore recovery copy: %w", err)
	}
	if _, _, err := verifyAuthFile(s.authPath(), recoveryBytes, recoveryIdentity); err != nil {
		return fmt.Errorf("recovery copy was installed, but verification failed: %w", err)
	}
	data.ActiveID = recoveryProfile.ID
	if err := s.saveState(data); err != nil {
		return fmt.Errorf("file was restored, but the index could not be saved: %w", err)
	}
	return nil
}

func (s *Store) installTarget(data state, target Profile, targetBytes, expectedCurrent []byte) (bool, error) {
	if len(expectedCurrent) != 0 {
		currentBytes, _, err := s.readCurrent()
		if err != nil {
			return false, err
		}
		if sha256.Sum256(currentBytes) != sha256.Sum256(expectedCurrent) {
			return false, ErrCurrentChanged
		}
	}
	if err := atomicWrite(s.authPath(), targetBytes, 0o600); err != nil {
		return false, fmt.Errorf("replace auth.json: %w", err)
	}
	return s.finishSwitch(data, target, targetBytes)
}

func (s *Store) finishSwitch(data state, target Profile, expected []byte) (bool, error) {
	_, gotIdentity, err := verifyAuthFile(s.authPath(), expected, identity{AccountID: "", Subject: ""})
	if err != nil {
		return false, fmt.Errorf("file was installed, but verification failed: %w", err)
	}
	if gotIdentity.key() != target.Identity {
		return false, errors.New("file was installed, but verification found a different identity")
	}
	data.ActiveID = target.ID
	if err := s.saveState(data); err != nil {
		return false, fmt.Errorf("auth.json was replaced, but the index could not be saved: %w", err)
	}
	return false, nil
}

func (s *Store) currentProfile(data state) (string, bool, error) {
	_, id, err := s.readCurrent()
	if errors.Is(err, ErrNoAuth) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	profile, ok := findIdentity(data, id.key())
	if !ok {
		return "", true, nil
	}
	return profile.ID, true, nil
}

func (s *Store) readCurrent() ([]byte, identity, error) {
	raw, id, err := readAuth(s.authPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, identity{}, ErrNoAuth
	}
	return raw, id, err
}

func (s *Store) loadState() (state, error) {
	path := s.statePath()
	raw, err := readRegularFile(path, maxAuthSize)
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: 1}, nil
	}
	if err != nil {
		return state{}, fmt.Errorf("read profile index: %w", err)
	}
	var data state
	if err := json.Unmarshal(raw, &data); err != nil {
		return state{}, errors.New("profile index is invalid; auth.json was not changed")
	}
	if data.Version != 1 {
		return state{}, fmt.Errorf("unsupported profile index version: %d", data.Version)
	}
	ids := make(map[string]struct{}, len(data.Profiles))
	names := make(map[string]struct{}, len(data.Profiles))
	identities := make(map[string]struct{}, len(data.Profiles))
	for _, profile := range data.Profiles {
		if !validID(profile.ID) || validateName(profile.Name) != nil || profile.Identity == "" {
			return state{}, errors.New("profile index contains an invalid profile; auth.json was not changed")
		}
		if _, ok := ids[profile.ID]; ok {
			return state{}, errors.New("profile index contains a duplicate profile ID")
		}
		if _, ok := names[profile.Name]; ok {
			return state{}, errors.New("profile index contains a duplicate profile name")
		}
		if _, ok := identities[profile.Identity]; ok {
			return state{}, errors.New("profile index contains a duplicate account identity")
		}
		ids[profile.ID] = struct{}{}
		names[profile.Name] = struct{}{}
		identities[profile.Identity] = struct{}{}
	}
	return data, nil
}

func (data *state) markSaved(profileID string) {
	for i := range data.Profiles {
		if data.Profiles[i].ID == profileID {
			data.Profiles[i].SavedAt = time.Now().UTC().Format(time.RFC3339)
			return
		}
	}
}

func (s *Store) saveState(data state) error {
	data.Version = 1
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.statePath(), append(content, '\n'), 0o600)
}

func (s *Store) profilePath(id string) string {
	return filepath.Join(s.root, "profiles", id+".json")
}

func (s *Store) statePath() string    { return filepath.Join(s.root, "state.json") }
func (s *Store) authPath() string     { return filepath.Join(s.codexHome, "auth.json") }
func (s *Store) recoveryPath() string { return filepath.Join(s.root, "recovery", "previous.json") }

func (s *Store) ensureDirs() error {
	for _, path := range []string{s.root, filepath.Join(s.root, "profiles"), filepath.Join(s.root, "recovery")} {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create data directory: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect data directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a regular directory", path)
		}
		if err := secureDataDir(path); err != nil {
			return fmt.Errorf("secure data directory: %w", err)
		}
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("profile name cannot be empty")
	}
	if len([]rune(name)) > 40 {
		return errors.New("profile name cannot exceed 40 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("profile name cannot contain control characters")
		}
	}
	return nil
}

func readAuth(path string) ([]byte, identity, error) {
	raw, err := readRegularFile(path, maxAuthSize)
	if err != nil {
		return nil, identity{}, err
	}
	var auth authFile
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, identity{}, fmt.Errorf("%w: file does not contain valid JSON", ErrInvalidAuth)
	}
	if auth.Mode != "chatgpt" || auth.Tokens.AccountID == "" || auth.Tokens.IDToken == "" || auth.Tokens.AccessToken == "" || auth.Tokens.RefreshToken == "" {
		return nil, identity{}, fmt.Errorf("%w: only complete file-based ChatGPT sign-in is supported", ErrInvalidAuth)
	}
	subject, err := idTokenSubject(auth.Tokens.IDToken)
	if err != nil {
		return nil, identity{}, fmt.Errorf("%w: cannot determine account identity from id_token", ErrInvalidAuth)
	}
	return raw, identity{AccountID: auth.Tokens.AccountID, Subject: subject}, nil
}

func idTokenSubject(token string) (string, error) {
	if len(token) > 64*1024 {
		return "", errors.New("id_token is too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("id_token has an invalid format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Subject == "" {
		return "", errors.New("id_token does not contain a subject")
	}
	return claims.Subject, nil
}

func readRegularFile(path string, limit int64) (data []byte, retErr error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close %s: %w", path, err))
		}
	}()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s changed while opening", path)
	}
	if after.Size() > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", path, limit)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", path, limit)
	}
	return raw, nil
}

func atomicWrite(path string, content []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".codex-switch-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			if retErr == nil {
				retErr = fmt.Errorf("remove temporary file: %w", err)
			} else {
				retErr = errors.Join(retErr, fmt.Errorf("remove temporary file: %w", err))
			}
		}
	}()
	if err := secureTempFile(tmpPath, tmp, mode); err != nil {
		return errors.Join(err, tmp.Close())
	}
	n, err := tmp.Write(content)
	if err == nil && n != len(content) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err := atomicReplace(tmpPath, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func verifyAuthFile(path string, expected []byte, expectedIdentity identity) ([]byte, identity, error) {
	got, id, err := readAuth(path)
	if err != nil {
		return nil, identity{}, err
	}
	if !bytes.Equal(got, expected) {
		return nil, identity{}, errors.New("installed file differs from the prepared copy")
	}
	if expectedIdentity.AccountID != "" && id.key() != expectedIdentity.key() {
		return nil, identity{}, errors.New("installed copy has a different identity")
	}
	return got, id, nil
}

func (i identity) key() string { return i.AccountID + "\x00" + i.Subject }

func findProfile(data state, id string) (Profile, bool) {
	for _, profile := range data.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}

func findIdentity(data state, key string) (Profile, bool) {
	for _, profile := range data.Profiles {
		if profile.Identity == key {
			return profile, true
		}
	}
	return Profile{}, false
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate profile ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
