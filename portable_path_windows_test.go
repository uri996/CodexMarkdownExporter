package main

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOutputDirectoryWindowsPermissionDenied(t *testing.T) {
	isolatedHome(t)
	custom := t.TempDir()
	if err := saveOutputRoot(custom); err != nil {
		t.Fatal(err)
	}
	// Change permissions only on this synthetic directory and restore before cleanup.
	blocked := t.TempDir()
	original, err := windows.GetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	originalACL, _, err := original.DACL()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Deny FILE_ADD_FILE to everyone; retain owner control to restore the ACL.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(D;;0x2;;;WD)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.SetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
			t.Error("restore synthetic directory permissions:", err)
		}
	}()
	if err := saveOutputRoot(blocked); !errors.Is(err, os.ErrPermission) {
		t.Fatal("permission denial not reported", err)
	}
	if got, _ := savedOutputRoot(); got != custom {
		t.Fatal("failed directory selection changed existing default")
	}
}
