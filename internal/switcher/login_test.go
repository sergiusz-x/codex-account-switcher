package switcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsolatedLoginAddsAndRenewsWithoutChangingCurrentAccount(t *testing.T) {
	store := NewStore(t.TempDir())
	current := fakeAuth("account-a", "subject-a", "synthetic-current", "current-extra")
	writeTestAuth(t, store.authPath(), current)
	if _, err := store.SaveCurrent("Account A", false); err != nil {
		t.Fatal(err)
	}
	imported := fakeAuth("account-b", "subject-b", "synthetic-imported", "imported-extra")
	session := &Login{home: t.TempDir(), cancel: func() {}}
	writeTestAuth(t, filepath.Join(session.home, "auth.json"), imported)
	if _, err := session.Complete(store, "Account B", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(session.home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private login directory was not removed")
	}
	profiles, err := store.List()
	if err != nil || len(profiles) != 2 {
		t.Fatal("import did not add exactly one profile")
	}
	b := profiles[1]
	if string(readTestFile(t, store.profilePath(b.ID))) != string(imported) {
		t.Fatal("import changed the authentication file contents")
	}
	writeTestAuth(t, store.recoveryPath(), imported)
	renewed := fakeAuth("account-b", "subject-b", "synthetic-renewed", "renewed-extra")
	_, id, err := readAuthBytes(t, renewed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.importLogin("", b.ID, renewed, id); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.profilePath(b.ID), store.recoveryPath()} {
		if string(readTestFile(t, path)) != string(renewed) {
			t.Fatal("renewal did not update the saved profile and matching recovery copy")
		}
	}
	if _, err := store.importLogin("", b.ID, current, identity{AccountID: "account-a", Subject: "subject-a"}); err == nil {
		t.Fatal("renewal accepted a different account")
	}
	if string(readTestFile(t, store.profilePath(b.ID))) != string(renewed) {
		t.Fatal("rejected renewal changed the saved login")
	}
	if string(readTestFile(t, store.authPath())) != string(current) {
		t.Fatal("adding or renewing an inactive account changed the current login")
	}
}

func TestCanceledLoginRemovesTemporaryCredentialsWithoutImporting(t *testing.T) {
	store := NewStore(t.TempDir())
	session := &Login{home: t.TempDir(), cancel: func() {}}
	writeTestAuth(t, filepath.Join(session.home, "auth.json"), fakeAuth("account-b", "subject-b", "synthetic-canceled", ""))
	if _, err := session.Complete(store, "Account B", "", errors.New("synthetic process failure")); err == nil {
		t.Fatal("failed login was accepted")
	}
	if _, err := os.Stat(session.home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled login left temporary credentials")
	}
	if profiles, err := store.List(); err != nil || len(profiles) != 0 {
		t.Fatal("canceled login changed saved profiles")
	}
	if _, err := os.Stat(store.authPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled login created an active authentication file")
	}
}
