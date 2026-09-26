//go:build windows

// Item 2li (b): the trade, in code. A machine-scoped DPAPI blob is decryptable
// by anything running on this machine -- rel-1.15.0/W0 measured that its master
// key is not in any user's key store -- so the DPAPI envelope is not what keeps
// the service password secret. THE FILE'S ACL IS. Everything here exists to make
// that ACL explicit when the blob is written and to refuse the blob when the ACL
// no longer holds, so a widened permission fails closed instead of quietly
// handing the password to whoever widened it.

package credential

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The three principals a machine-scoped service credential may be readable by,
// and nothing else: the local Administrators group, the SYSTEM account, and the
// service account whose password it holds.
var machineBlobPrincipals = []string{"BA", "SY"}

// machineBlobDescriptor builds the SDDL for the blob. `P` protects the DACL, so
// nothing is inherited from the data directory: the list below is the whole list.
func machineBlobDescriptor(accountSID string) string {
	var sddl strings.Builder
	sddl.WriteString("D:P")
	for _, principal := range machineBlobPrincipals {
		fmt.Fprintf(&sddl, "(A;;FA;;;%s)", principal)
	}
	if accountSID != "" {
		fmt.Fprintf(&sddl, "(A;;FR;;;%s)", accountSID)
	}
	return sddl.String()
}

// lookupAccountSID resolves a local account name to its SID string. An account
// that cannot be resolved is not a reason to write a blob nobody can read, so
// the caller treats the error as a refusal.
func lookupAccountSID(account string) (string, error) {
	if strings.TrimSpace(account) == "" {
		return "", nil
	}
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", account, err)
	}
	return sid.String(), nil
}

// secureMachineBlob replaces the file's DACL with the protected one above. It is
// called immediately after the blob is written and before it is published, so a
// reader never sees the file under inherited permissions.
func secureMachineBlob(path, account string) error {
	accountSID, err := lookupAccountSID(account)
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString(machineBlobDescriptor(accountSID))
	if err != nil {
		return fmt.Errorf("build the machine credential descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read the machine credential descriptor: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect the machine credential file: %w", err)
	}
	return nil
}

// machineBlobACLHolds is the read-side half of (b). It answers one question --
// can anyone outside the three principals reach this file? -- and answers it by
// reading the DACL that is actually on disk rather than trusting what was
// written. An unprotected DACL is a failure too: inheritance can add a principal
// without touching a single ACE here.
func machineBlobACLHolds(path, account string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the machine credential permissions: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read the machine credential control flags: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("%w: its permissions are inherited rather than protected", ErrMachineBlobUnprotected)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read the machine credential permissions: %w", err)
	}
	if dacl == nil {
		// A nil DACL grants everyone everything. Nothing about that is safe.
		return fmt.Errorf("%w: it has no access list at all", ErrMachineBlobUnprotected)
	}
	allowed := map[string]bool{}
	for _, principal := range machineBlobPrincipals {
		sid, err := windows.CreateWellKnownSid(wellKnown(principal))
		if err != nil {
			return fmt.Errorf("resolve the expected principals: %w", err)
		}
		allowed[sid.String()] = true
	}
	if accountSID, err := lookupAccountSID(account); err == nil && accountSID != "" {
		allowed[accountSID] = true
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read the machine credential permissions: %w", err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !allowed[sid.String()] {
			return fmt.Errorf("%w: %s can reach it", ErrMachineBlobUnprotected, sid.String())
		}
	}
	return nil
}

func wellKnown(principal string) windows.WELL_KNOWN_SID_TYPE {
	switch principal {
	case "BA":
		return windows.WinBuiltinAdministratorsSid
	default:
		return windows.WinLocalSystemSid
	}
}
