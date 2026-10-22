package switcher

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCredentialSavesUpdateProfileTimestamp(t *testing.T) {
	for _, action := range []string{"replace", "same account", "switch", "restore"} {
		t.Run(action, func(t *testing.T) {
			store := NewStore(t.TempDir())
			writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "synthetic-a", ""))
			a, err := store.SaveCurrent("Account A", false)
			if err != nil {
				t.Fatal(err)
			}
			writeTestAuth(t, store.authPath(), fakeAuth("account-b", "subject-b", "synthetic-b", ""))
			b, err := store.SaveCurrent("Account B", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Switch(a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Switch(b.ID); err != nil {
				t.Fatal(err)
			}
			data, err := store.loadState()
			if err != nil {
				t.Fatal(err)
			}
			const old = "2000-01-01T00:00:00Z"
			for i := range data.Profiles {
				data.Profiles[i].SavedAt = old
			}
			if err := store.saveState(data); err != nil {
				t.Fatal(err)
			}
			refreshed := fakeAuth("account-b", "subject-b", "synthetic-b-refreshed", "preserved")
			writeTestAuth(t, store.authPath(), refreshed)
			switch action {
			case "replace":
				_, err = store.SaveCurrent(b.Name, true)
			case "same account":
				_, err = store.Switch(b.ID)
			case "switch":
				_, err = store.Switch(a.ID)
			case "restore":
				err = store.Restore()
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err = store.loadState()
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := findProfile(data, b.ID)
			if _, err := time.Parse(time.RFC3339, saved.SavedAt); err != nil || saved.SavedAt == old {
				t.Fatal("saved profile timestamp was not updated")
			}
			unchanged, _ := findProfile(data, a.ID)
			if unchanged.SavedAt != old {
				t.Fatal("an unrelated profile timestamp changed")
			}
			if string(readTestFile(t, store.profilePath(b.ID))) != string(refreshed) {
				t.Fatal("refreshed credentials or unknown fields were lost")
			}
		})
	}
}

func TestSaveSwitchRefreshAndRestore(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	accountA1 := fakeAuth("account-a", "subject-a", "access-a-1", "extra-a")
	writeTestAuth(t, store.authPath(), accountA1)
	profileA, err := store.SaveCurrent("Account A", false)
	if err != nil {
		t.Fatal(err)
	}

	accountA2 := fakeAuth("account-a", "subject-a", "access-a-2", "extra-a")
	writeTestAuth(t, store.authPath(), accountA2)
	already, err := store.Switch(profileA.ID)
	if err != nil || !already {
		t.Fatalf("switch to active profile: already=%v err=%v", already, err)
	}
	if got := readTestFile(t, store.profilePath(profileA.ID)); string(got) != string(accountA2) {
		t.Fatal("selecting the active profile did not save its refreshed tokens and unknown fields")
	}

	accountB1 := fakeAuth("account-b", "subject-b", "access-b-1", "extra-b")
	writeTestAuth(t, store.authPath(), accountB1)
	profileB, err := store.SaveCurrent("Account B", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(profileA.ID); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(accountA2) {
		t.Fatal("did not install the saved A profile")
	}
	if got := readTestFile(t, store.profilePath(profileB.ID)); string(got) != string(accountB1) {
		t.Fatal("did not preserve the current B profile")
	}

	accountA3 := fakeAuth("account-a", "subject-a", "access-a-refreshed", "unknown-field-still-here")
	writeTestAuth(t, store.authPath(), accountA3)
	if _, err := store.Switch(profileB.ID); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, store.profilePath(profileA.ID)); string(got) != string(accountA3) {
		t.Fatal("switch did not save the latest refreshed A tokens")
	}
	if err := store.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(accountA3) {
		t.Fatal("restore did not recover the exact previous bytes")
	}
}

func TestUnknownCurrentIsNotOverwritten(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "a", ""))
	profile, err := store.SaveCurrent("Account A", false)
	if err != nil {
		t.Fatal(err)
	}
	unknown := fakeAuth("account-b", "subject-b", "b", "")
	writeTestAuth(t, store.authPath(), unknown)

	if _, err := store.Switch(profile.ID); !errors.Is(err, ErrUnknownCurrent) {
		t.Fatalf("Switch error = %v, want ErrUnknownCurrent", err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(unknown) {
		t.Fatal("unknown current login was overwritten")
	}
}

func TestSyncCurrentPreservesReloginForItsOwner(t *testing.T) {
	store := NewStore(t.TempDir())
	oldA := fakeAuth("account-a", "subject-a", "old-a", "")
	writeTestAuth(t, store.authPath(), oldA)
	profileA, err := store.SaveCurrent("Account A", false)
	if err != nil {
		t.Fatal(err)
	}
	accountB := fakeAuth("account-b", "subject-b", "b", "")
	writeTestAuth(t, store.authPath(), accountB)
	profileB, err := store.SaveCurrent("Account B", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.loadState()
	if err != nil {
		t.Fatal(err)
	}
	data.ActiveID = profileB.ID // A stale marker must not determine the update destination.
	data.Profiles[0].SavedAt = "2020-01-01T00:00:00Z"
	if err := store.saveState(data); err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.recoveryPath(), accountB)
	current := fakeAuth("account-a", "subject-a", "new-login-a", "preserve-unknown-field")
	writeTestAuth(t, store.authPath(), current)
	activeInfo, err := os.Stat(store.authPath())
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.SyncCurrent()
	if err != nil || !updated {
		t.Fatalf("SyncCurrent: updated=%v err=%v", updated, err)
	}
	for path, want := range map[string][]byte{
		store.authPath(): current, store.profilePath(profileA.ID): current,
		store.profilePath(profileB.ID): accountB, store.recoveryPath(): accountB,
	} {
		if got := readTestFile(t, path); string(got) != string(want) {
			t.Fatalf("unexpected contents in %s", filepath.Base(path))
		}
	}
	newActiveInfo, err := os.Stat(store.authPath())
	if err != nil || !os.SameFile(activeInfo, newActiveInfo) {
		t.Fatal("synchronization replaced the active auth file")
	}
	data, err = store.loadState()
	if err != nil || data.Profiles[0].SavedAt == "2020-01-01T00:00:00Z" {
		t.Fatalf("saved timestamp was not updated: %v", err)
	}
	indexBefore := readTestFile(t, store.statePath())
	if updated, err := store.SyncCurrent(); err != nil || updated {
		t.Fatalf("unchanged login: updated=%v err=%v", updated, err)
	}
	if string(readTestFile(t, store.statePath())) != string(indexBefore) {
		t.Fatal("unchanged login rewrote the index")
	}
}

func TestSyncCurrentDoesNotOverwriteUnrelatedOrInvalidProfiles(t *testing.T) {
	for _, scenario := range []string{"unknown account", "different subject", "invalid current", "missing current", "mismatched saved identity"} {
		t.Run(scenario, func(t *testing.T) {
			store := NewStore(t.TempDir())
			original := fakeAuth("account-a", "subject-a", "original", "")
			writeTestAuth(t, store.authPath(), original)
			profile, err := store.SaveCurrent("Account A", false)
			if err != nil {
				t.Fatal(err)
			}
			current := original
			wantError := false
			switch scenario {
			case "unknown account":
				current = fakeAuth("account-b", "subject-b", "other", "")
			case "different subject":
				current = fakeAuth("account-a", "subject-other", "other", "")
			case "invalid current":
				current, wantError = []byte("invalid"), true
			case "mismatched saved identity":
				writeTestAuth(t, store.profilePath(profile.ID), fakeAuth("account-b", "subject-b", "other", ""))
				wantError = true
			}
			writeTestAuth(t, store.authPath(), current)
			if scenario == "missing current" {
				if err := os.Remove(store.authPath()); err != nil {
					t.Fatal(err)
				}
			}
			savedBefore := readTestFile(t, store.profilePath(profile.ID))
			indexBefore := readTestFile(t, store.statePath())
			updated, err := store.SyncCurrent()
			if updated || (err != nil) != wantError {
				t.Fatalf("SyncCurrent: updated=%v err=%v; want error=%v", updated, err, wantError)
			}
			if string(readTestFile(t, store.profilePath(profile.ID))) != string(savedBefore) ||
				string(readTestFile(t, store.statePath())) != string(indexBefore) {
				t.Fatal("synchronization changed saved data")
			}
			if scenario != "missing current" && string(readTestFile(t, store.authPath())) != string(current) {
				t.Fatal("synchronization changed the active login")
			}
		})
	}
}

func TestSaveRequiresExplicitConfirmationForExistingIdentity(t *testing.T) {
	store := NewStore(t.TempDir())
	first := fakeAuth("account-a", "subject-a", "first", "")
	writeTestAuth(t, store.authPath(), first)
	if _, err := store.SaveCurrent("Account A", false); err != nil {
		t.Fatal(err)
	}
	second := fakeAuth("account-a", "subject-a", "refreshed", "future-field")
	writeTestAuth(t, store.authPath(), second)
	if _, err := store.SaveCurrent("Account A", false); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("SaveCurrent error = %v, want ErrProfileExists", err)
	}
	if _, err := store.SaveCurrent("Account A", true); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.List()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles=%v err=%v", profiles, err)
	}
	if got := readTestFile(t, store.profilePath(profiles[0].ID)); string(got) != string(second) {
		t.Fatal("confirmed update did not preserve the new auth bytes")
	}
}

func TestRenameProfileChangesNameWithoutChangingCredentials(t *testing.T) {
	store := NewStore(t.TempDir())
	auth := fakeAuth("account-a", "subject-a", "access-a", "extra-a")
	writeTestAuth(t, store.authPath(), auth)
	profile, err := store.SaveCurrent("Personal", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RenameProfile(profile.ID, "Private"); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.List()
	if err != nil || len(profiles) != 1 || profiles[0].Name != "Private" {
		t.Fatalf("profiles after rename = %v, err = %v", profiles, err)
	}
	if got := readTestFile(t, store.profilePath(profile.ID)); string(got) != string(auth) {
		t.Fatal("renaming changed the saved credentials")
	}
	if err := store.RenameProfile(profile.ID, "Private"); err != nil {
		t.Fatalf("renaming to the same name failed: %v", err)
	}
}

func TestRenameProfileRejectsDuplicateName(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "a", ""))
	profileA, err := store.SaveCurrent("Personal", false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.authPath(), fakeAuth("account-b", "subject-b", "b", ""))
	profileB, err := store.SaveCurrent("Work", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RenameProfile(profileB.ID, profileA.Name); !errors.Is(err, ErrProfileNameTaken) {
		t.Fatalf("RenameProfile error = %v, want ErrProfileNameTaken", err)
	}
}

func TestDeleteProfilePreservesCurrentLoginAndMatchingRecovery(t *testing.T) {
	store := NewStore(t.TempDir())
	authA := fakeAuth("account-a", "subject-a", "a", "")
	writeTestAuth(t, store.authPath(), authA)
	profileA, err := store.SaveCurrent("Personal", false)
	if err != nil {
		t.Fatal(err)
	}
	authB := fakeAuth("account-b", "subject-b", "b", "")
	writeTestAuth(t, store.authPath(), authB)
	profileB, err := store.SaveCurrent("Work", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(profileA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(profileB.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteProfile(profileA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.profilePath(profileA.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted profile file still exists: err=%v", err)
	}
	if hasRecovery, err := store.HasRecovery(); err != nil || hasRecovery {
		t.Fatalf("matching recovery copy remains: hasRecovery=%v err=%v", hasRecovery, err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(authB) {
		t.Fatal("deleting a profile changed the current login")
	}
	profiles, err := store.List()
	if err != nil || len(profiles) != 1 || profiles[0].ID != profileB.ID {
		t.Fatalf("profiles after delete = %v, err = %v", profiles, err)
	}
}

func TestDeleteActiveProfileLeavesCurrentLoginInPlace(t *testing.T) {
	store := NewStore(t.TempDir())
	auth := fakeAuth("account-a", "subject-a", "a", "")
	writeTestAuth(t, store.authPath(), auth)
	profile, err := store.SaveCurrent("Personal", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteProfile(profile.ID); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(auth) {
		t.Fatal("deleting the active profile changed auth.json")
	}
	if id, exists, err := store.ActiveID(); err != nil || !exists || id != "" {
		t.Fatalf("active profile after delete = %q, %v, %v; want unsaved existing login", id, exists, err)
	}
}

func TestSameIdentityCannotBeRenamedBySave(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "first", ""))
	if _, err := store.SaveCurrent("Account A", false); err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "refreshed", ""))
	if _, err := store.SaveCurrent("New name", false); !errors.Is(err, ErrProfileNameDiff) {
		t.Fatalf("SaveCurrent error = %v, want ErrProfileNameDiff", err)
	}
	if _, err := store.SaveCurrent("New name", true); !errors.Is(err, ErrProfileNameDiff) {
		t.Fatalf("confirmed SaveCurrent error = %v, want ErrProfileNameDiff", err)
	}
}

func TestSameNameCannotReplaceDifferentIdentity(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "a", ""))
	if _, err := store.SaveCurrent("Account A", false); err != nil {
		t.Fatal(err)
	}
	second := fakeAuth("account-b", "subject-b", "b", "")
	writeTestAuth(t, store.authPath(), second)
	if _, err := store.SaveCurrent("Account A", true); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("SaveCurrent error = %v, want ErrNameTaken", err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(second) {
		t.Fatal("name collision changed the active auth file")
	}
}

func TestInvalidTargetAndIndexDoNotChangeCurrentAuth(t *testing.T) {
	t.Run("invalid target", func(t *testing.T) {
		store := NewStore(t.TempDir())
		current := fakeAuth("account-a", "subject-a", "a", "")
		writeTestAuth(t, store.authPath(), current)
		profile, err := store.SaveCurrent("Account A", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.profilePath(profile.ID), []byte(`{"invalid":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Switch(profile.ID); err == nil {
			t.Fatal("Switch succeeded with an invalid target")
		}
		if got := readTestFile(t, store.authPath()); string(got) != string(current) {
			t.Fatal("invalid target changed the current auth file")
		}
	})

	t.Run("invalid index", func(t *testing.T) {
		store := NewStore(t.TempDir())
		current := fakeAuth("account-a", "subject-a", "a", "")
		writeTestAuth(t, store.authPath(), current)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.statePath(), []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveCurrent("Account A", false); err == nil {
			t.Fatal("SaveCurrent succeeded with an invalid index")
		}
		if got := readTestFile(t, store.authPath()); string(got) != string(current) {
			t.Fatal("invalid index changed the current auth file")
		}
	})
}

func TestCurrentCorruptionKeepsProfilesVisibleAndCanRestore(t *testing.T) {
	store := NewStore(t.TempDir())
	first := fakeAuth("account-a", "subject-a", "a", "")
	writeTestAuth(t, store.authPath(), first)
	profileA, err := store.SaveCurrent("Account A", false)
	if err != nil {
		t.Fatal(err)
	}
	accountB := fakeAuth("account-b", "subject-b", "b", "")
	writeTestAuth(t, store.authPath(), accountB)
	profileB, err := store.SaveCurrent("Account B", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(profileA.ID); err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.authPath(), []byte("broken"))
	profiles, err := store.List()
	if err != nil || len(profiles) != 2 {
		t.Fatalf("List should remain available when auth.json is corrupt: profiles=%d err=%v", len(profiles), err)
	}
	if _, _, err := store.ActiveID(); err == nil {
		t.Fatal("ActiveID succeeded for corrupt auth.json")
	}
	if err := store.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(accountB) {
		t.Fatal("Restore did not reinstall the previous valid profile")
	}
	if got := readTestFile(t, store.profilePath(profileB.ID)); string(got) != string(accountB) {
		t.Fatal("recovery damaged the saved profile")
	}
	recoveryFiles, err := filepath.Glob(filepath.Join(store.root, "recovery", "invalid-current-*.json"))
	if err != nil || len(recoveryFiles) != 1 {
		t.Fatalf("corrupt login was not retained for manual recovery: files=%v err=%v", recoveryFiles, err)
	}
	if got := readTestFile(t, recoveryFiles[0]); string(got) != "broken" {
		t.Fatal("corrupt login backup does not match the original bytes")
	}
}

func TestRestoreRejectsRecoveryForUnknownAccount(t *testing.T) {
	store := NewStore(t.TempDir())
	current := fakeAuth("account-a", "subject-a", "current", "")
	writeTestAuth(t, store.authPath(), current)
	if _, err := store.SaveCurrent("Account A", false); err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.recoveryPath(), fakeAuth("account-b", "subject-b", "unknown", ""))

	if err := store.Restore(); !errors.Is(err, ErrNoRecovery) {
		t.Fatalf("Restore error = %v, want ErrNoRecovery", err)
	}
	if got := readTestFile(t, store.authPath()); string(got) != string(current) {
		t.Fatal("unknown recovery account changed the current auth file")
	}
}

func TestPermissionsAndSymlinkRejection(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "a", ""))
	profile, err := store.SaveCurrent("Account A", false)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Dir(store.statePath()):             0o700,
		filepath.Dir(store.profilePath(profile.ID)): 0o700,
		store.statePath():                           0o600,
		store.profilePath(profile.ID):               0o600,
	} {
		if runtime.GOOS == "windows" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("permissions for %s = %o, want %o", path, got, want)
		}
	}

	linkPath := filepath.Join(t.TempDir(), "linked.json")
	if err := os.Symlink(store.profilePath(profile.ID), linkPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Logf("symlink test skipped because this Windows account cannot create symlinks: %v", err)
			return
		}
		t.Fatal(err)
	}
	if _, _, err := readAuth(linkPath); err == nil {
		t.Fatal("readAuth accepted a symlink")
	}
}

func TestLockPreventsConcurrentSwitcher(t *testing.T) {
	store := NewStore(t.TempDir())
	unlock, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := store.lock(); err == nil || !strings.Contains(err.Error(), "another switcher") {
		t.Fatalf("second lock error = %v, want occupied-lock error", err)
	}
}

func TestRejectsMalformedAuthAndUnsafeNames(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("not-json"), []byte(`{"auth_mode":"api_key"}`), []byte(`{"auth_mode":"chatgpt","tokens":{}}`)} {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readAuth(path); err == nil {
			t.Errorf("readAuth accepted invalid auth data: %s", path)
		}
	}
	for _, name := range []string{"", " \t", "a\nb", strings.Repeat("a", 41)} {
		if err := validateName(name); err == nil {
			t.Errorf("validateName accepted %q", name)
		}
	}
}

func TestIdentityIsBasedOnAccountAndSubjectNotTokens(t *testing.T) {
	first := fakeAuth("account-a", "subject-a", "access-one", "")
	second := fakeAuth("account-a", "subject-a", "access-two", "")
	_, firstID, err := readAuthBytes(t, first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondID, err := readAuthBytes(t, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstID.key() != secondID.key() {
		t.Fatal("token refresh changed the account identity")
	}
	if sha256.Sum256(first) == sha256.Sum256(second) {
		t.Fatal("test auth fixtures should contain different refreshed tokens")
	}
}

func TestReadLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxAuthSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFile(path, maxAuthSize); err == nil {
		t.Fatal("readRegularFile accepted a file over the size limit")
	}
}

func writeTestAuth(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fakeAuth(account, subject, access, extra string) []byte {
	payload, _ := json.Marshal(map[string]string{"sub": subject})
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	value := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"id_token": token, "access_token": access, "refresh_token": "refresh-" + access, "account_id": account,
		},
		"last_refresh": "2026-10-21T00:00:00Z",
	}
	if extra != "" {
		value["future_field"] = extra
	}
	data, _ := json.Marshal(value)
	return append(data, '\n')
}

func readAuthBytes(t *testing.T, raw []byte) ([]byte, identity, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, identity{}, err
	}
	return readAuth(path)
}
