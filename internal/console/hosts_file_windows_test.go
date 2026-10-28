package console

import (
	"bytes"
	"io"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHostsSessionReplacementFailureKeepsOriginal(t *testing.T) {
	h, _ := fixtureHostsController(t, hostsBaseline)
	path, err := windows.UTF16PtrFromString(h.path)
	if err != nil {
		t.Fatal(err)
	}
	// Another reader permits in-place writes but denies replacement. The old
	// truncating implementation would succeed instead of preserving this file.
	locked, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(locked)
	if err := h.change("enable", true); err == nil {
		t.Fatal("hosts were changed despite denied replacement")
	}
	if err := h.change("rollback", true); err != nil {
		t.Fatal("failed write could not be rolled back", err)
	}
	if err := h.close(true); err != nil {
		t.Fatal(err)
	}
	if readHostsFixture(t, h.path) != hostsBaseline {
		t.Fatal("failed replacement or recovery changed original hosts")
	}
}

func TestHostsCommitFailureKeepsOriginal(t *testing.T) {
	path := hostsFixture(t, hostsBaseline)
	var locked windows.Handle
	defer func() {
		if locked != 0 {
			windows.CloseHandle(locked)
		}
	}()
	contents := hostsReadFunc(func(p []byte) (int, error) {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		// Acquire the sharing conflict only after staging/preflight succeeded.
		locked, err = windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return copy(p, "complete replacement"), io.EOF
	})
	if err := replaceHostsFile(path, []byte(hostsBaseline), contents); err == nil {
		t.Fatal("replacement ignored an open reader denying deletion")
	}
	if readHostsFixture(t, path) != hostsBaseline {
		t.Fatal("failed rename changed hosts")
	}
	assertNoHostsStages(t, path)
	if err := windows.CloseHandle(locked); err != nil {
		t.Fatal(err)
	}
	locked = 0
	if err := replaceHostsFile(path, []byte(hostsBaseline), bytes.NewReader([]byte("complete replacement"))); err != nil {
		t.Fatal("replacement could not be retried", err)
	}
}

func TestHostsReplacementPreservesDACL(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, protection := range []string{"", "P"} {
		t.Run("protected="+protection, func(t *testing.T) {
			h, _ := fixtureHostsController(t, hostsBaseline)
			sd, err := windows.SecurityDescriptorFromString("D:" + protection + "(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;BU)")
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
			if protection == "P" {
				flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
			}
			if err := windows.SetNamedSecurityInfo(h.path, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			readDACL := func() string {
				sd, err := windows.GetNamedSecurityInfo(h.path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
				if err != nil {
					t.Fatal(err)
				}
				return sd.String()
			}
			before := readDACL()
			for _, action := range []string{"enable", "rollback", "enable", "disable"} {
				if err := h.change(action, true); err != nil {
					t.Fatal(action, err)
				}
				if got := readDACL(); got != before {
					t.Fatalf("%s changed DACL: %s -> %s", action, before, got)
				}
			}
			if readHostsFixture(t, h.path) != hostsBaseline {
				t.Fatal("recovery changed user content")
			}
			assertNoHostsStages(t, h.path)
		})
	}
}

func TestHostsReplacementDoesNotBypassReadOnlyAttribute(t *testing.T) {
	path := hostsFixture(t, hostsBaseline)
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if err := applyHosts(path, "enable"); err == nil {
		t.Fatal("replaced read-only hosts")
	}
	if readHostsFixture(t, path) != hostsBaseline {
		t.Fatal("read-only content changed")
	}
	assertNoHostsStages(t, path)
}
