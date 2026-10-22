//go:build windows

package switcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsSecretACLsArePrivate(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestAuth(t, store.authPath(), fakeAuth("account-a", "subject-a", "a", ""))
	profileA, err := store.SaveCurrent("Account A", false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, store.authPath(), fakeAuth("account-b", "subject-b", "b", ""))
	profileB, err := store.SaveCurrent("Account B", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(profileA.ID); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	if sid == "" {
		t.Fatal("current user SID is empty")
	}
	for path, inheritance := range map[string]string{
		filepath.Dir(store.statePath()):              "OICI",
		filepath.Dir(store.profilePath(profileA.ID)): "OICI",
		store.statePath():                            "",
		store.profilePath(profileA.ID):               "",
		store.profilePath(profileB.ID):               "",
		store.authPath():                             "",
	} {
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatalf("read ACL for %s: %v", path, err)
		}
		sddl := descriptor.String()
		wantUserACE := fmt.Sprintf("(A;%s;FA;;;%s)", inheritance, sid)
		for _, want := range []string{"D:P", wantUserACE, "(A;" + inheritance + ";FA;;;SY)", "(A;" + inheritance + ";FA;;;BA)"} {
			if !strings.Contains(sddl, want) {
				t.Errorf("ACL for %s does not contain %q", path, want)
			}
		}
		if strings.Count(sddl, "(A;") != 3 {
			t.Errorf("ACL for %s grants access to unexpected principals: %s", path, sddl)
		}
	}
}

func TestWindowsReusedBroadDirectoryGetsProtected(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.Mkdir(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := openSecurePath(store.root, true)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;WD)")
	if err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err == nil {
		err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	}
	closeErr := windows.CloseHandle(handle)
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	if err := store.ensureDirs(); err != nil {
		t.Fatal(err)
	}
	secured, err := windows.GetNamedSecurityInfo(store.root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	sddl := secured.String()
	if !strings.Contains(sddl, "D:P") || strings.Contains(sddl, "WD") || strings.Count(sddl, "(A;") != 3 {
		t.Fatalf("reused directory ACL was not narrowed: %s", sddl)
	}
}

func TestWindowsJunctionIsRejected(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	target := filepath.Join(root, "junction-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(store.root), 0o700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	quote := func(value string) string { return strings.ReplaceAll(value, "'", "''") }
	command := fmt.Sprintf("$ErrorActionPreference='Stop'; New-Item -ItemType Junction -Path '%s' -Target '%s' | Out-Null", quote(store.root), quote(target))
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create no-admin junction fixture: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if err := store.ensureDirs(); err == nil {
		t.Fatal("store accepted a junction as its private root")
	}
}

func TestWindowsReplacementPreservesDestinationOnSharingViolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	old := []byte("synthetic previous credentials")
	if err := atomicWrite(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("synthetic replacement credentials")
	if err := atomicWrite(path, newBytes, 0o600); err == nil {
		t.Fatal("replacement succeeded while another process denied delete sharing")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(old) {
		t.Fatalf("sharing violation changed the active file: %q, %v", got, err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, newBytes, 0o600); err != nil {
		t.Fatalf("replacement after releasing the handle: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != string(newBytes) {
		t.Fatalf("replacement bytes = %q, %v", got, err)
	}
}

func TestWindowsLockCoordinatesSeparateProcessesAndReleasesAfterCrash(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	unlock, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	busy := exec.Command(os.Args[0], "-test.run=^TestWindowsLockProcessHelper$", "--", "busy", home, "")
	busy.Env = append(os.Environ(), "SWITCHER_LOCK_HELPER=1")
	if output, err := busy.CombinedOutput(); err != nil {
		t.Fatalf("contending process: %v: %s", err, output)
	}
	lockPath := filepath.Join(store.root, "switch.lock")
	if err := os.Remove(lockPath); err == nil {
		t.Fatal("lock file could be deleted while locked")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}

	ready := filepath.Join(home, "lock-ready")
	hold := exec.Command(os.Args[0], "-test.run=^TestWindowsLockProcessHelper$", "--", "hold", home, ready)
	hold.Env = append(os.Environ(), "SWITCHER_LOCK_HELPER=1")
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = hold.Process.Kill()
			_ = hold.Wait()
			t.Fatal("lock holder did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := hold.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := hold.Wait(); err == nil {
		t.Fatal("killed lock holder returned success")
	}
	unlock, err = store.lock()
	if err != nil {
		t.Fatalf("lock remained held after owner process exited: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsLockProcessHelper(t *testing.T) {
	if os.Getenv("SWITCHER_LOCK_HELPER") != "1" {
		return
	}
	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(args) < separator+4 {
		os.Exit(2)
	}
	mode, home, ready := args[separator+1], args[separator+2], args[separator+3]
	store := NewStore(home)
	unlock, err := store.lock()
	if mode == "busy" {
		if err != nil && strings.Contains(err.Error(), "another switcher is busy") {
			return
		}
		if unlock != nil {
			_ = unlock()
		}
		os.Exit(3)
	}
	if err != nil || mode != "hold" {
		os.Exit(4)
	}
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		_ = unlock()
		os.Exit(5)
	}
	for {
		time.Sleep(time.Hour)
	}
}
