//go:build windows

package switcher

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func codexIntegrationACLs(home string) string {
	current, _ := windows.GetCurrentProcessToken().GetTokenUser()
	currentSID := ""
	if current != nil {
		currentSID = current.User.Sid.String()
	}
	var summaries []string
	for _, name := range []string{"", "app-server-daemon", "app-server-control"} {
		path := home
		if name != "" {
			path = filepath.Join(home, name)
		}
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			summaries = append(summaries, fmt.Sprintf("%s=unavailable", name))
			continue
		}
		owner, _, err := descriptor.Owner()
		if err != nil || owner == nil {
			summaries = append(summaries, fmt.Sprintf("%s=owner-unavailable", name))
			continue
		}
		ownerLabel := "other"
		if current != nil && owner.Equals(current.User.Sid) {
			ownerLabel = "current"
		}
		sddl := strings.ReplaceAll(descriptor.String(), currentSID, "USER")
		summaries = append(summaries, fmt.Sprintf("%s owner=%s dacl=%s", name, ownerLabel, sddl))
	}
	return fmt.Sprint(summaries)
}

func secureCodexIntegrationDir(path string) (retErr error) {
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sddl := fmt.Sprintf("D:P(A;OICI;FA;;;%s)", current.User.Sid.String())
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, windows.CloseHandle(handle)) }()
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}
