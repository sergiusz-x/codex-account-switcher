//go:build windows

package switcher

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func syncDir(path string) (retErr error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open directory for flush: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return errors.Join(fmt.Errorf("inspect directory for flush: %w", err), windows.CloseHandle(handle))
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.Join(errors.New("directory for flush is not a regular directory"), windows.CloseHandle(handle))
	}
	flushErr := windows.FlushFileBuffers(handle)
	closeErr := windows.CloseHandle(handle)
	return errors.Join(flushErr, closeErr)
}

func secureDataDir(path string) (retErr error) {
	handle, err := openSecurePath(path, true)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, windows.CloseHandle(handle)) }()
	return secureHandle(handle, true)
}

func secureTempFile(path string, file *os.File, _ os.FileMode) error {
	if file == nil {
		return fmt.Errorf("secure %s: file handle is missing", path)
	}
	handle, err := openSecurePath(path, false)
	if err != nil {
		return err
	}
	secureErr := secureHandle(handle, false)
	closeErr := windows.CloseHandle(handle)
	return errors.Join(secureErr, closeErr)
}

func atomicReplace(source, destination string) error {
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", destination)
		}
		handle, err := openSecurePath(destination, false)
		if err != nil {
			return err
		}
		if err := windows.CloseHandle(handle); err != nil {
			return fmt.Errorf("close %s security handle: %w", destination, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func openSecurePath(path string, directory bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("open %s security descriptor: %w", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return windows.InvalidHandle, errors.Join(fmt.Errorf("inspect %s: %w", path, err), windows.CloseHandle(handle))
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	isReparse := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	if isReparse || isDirectory != directory {
		return windows.InvalidHandle, errors.Join(fmt.Errorf("%s is not a regular %s", path, map[bool]string{true: "directory", false: "file"}[directory]), windows.CloseHandle(handle))
	}
	return handle, nil
}

func secureHandle(handle windows.Handle, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current Windows user: %w", err)
	}
	sid := user.User.Sid.String()
	if sid == "" {
		return fmt.Errorf("format current Windows user SID")
	}
	currentSID := user.User.Sid
	existing, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect Windows object owner: %w", err)
	}
	owner, _, err := existing.Owner()
	if err != nil {
		return fmt.Errorf("read Windows object owner: %w", err)
	}
	if owner == nil || !owner.Equals(currentSID) {
		// Elevated Windows processes can create objects owned by Administrators.
		administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
		if err != nil {
			return fmt.Errorf("get Windows administrators SID: %w", err)
		}
		if owner == nil || !owner.Equals(administrators) || !windows.GetCurrentProcessToken().IsElevated() {
			return fmt.Errorf("refusing to secure a Windows object owned by another principal")
		}
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	sddl := fmt.Sprintf("D:P(A;%s;FA;;;%s)(A;%s;FA;;;SY)(A;%s;FA;;;BA)", inherit, sid, inherit, inherit)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("build private Windows ACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private Windows ACL: %w", err)
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("set private Windows ACL: %w", err)
	}
	return nil
}
