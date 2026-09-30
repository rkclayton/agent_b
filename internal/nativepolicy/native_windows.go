//go:build windows

package nativepolicy

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type AccountStatus struct {
	Exists, Enabled, Administrator, UsersMember, LockedOut bool
	SID                                                    string
}

type userInfo4 struct {
	Name, Password                        *uint16
	PasswordAge, Privilege                uint32
	HomeDir, Comment                      *uint16
	Flags                                 uint32
	ScriptPath                            *uint16
	AuthFlags                             uint32
	FullName, UserComment, Parameters     *uint16
	Workstations                          *uint16
	LastLogon, LastLogoff, AccountExpires uint32
	MaxStorage, UnitsPerWeek              uint32
	LogonHours                            *byte
	BadPasswordCount, LogonCount          uint32
	LogonServer                           *uint16
	CountryCode, CodePage                 uint32
	UserSID                               *windows.SID
	PrimaryGroupID                        uint32
	Profile, HomeDirDrive                 *uint16
	PasswordExpired                       uint32
}

type localGroupUsersInfo0 struct{ Name *uint16 }
type localGroupMembersInfo3 struct{ DomainAndName *uint16 }
type userInfo1 struct {
	Name, Password    *uint16
	PasswordAge, Priv uint32
	HomeDir, Comment  *uint16
	Flags             uint32
	ScriptPath        *uint16
}
type userInfo1003 struct{ Password *uint16 }

var (
	netapi32                  = windows.NewLazySystemDLL("netapi32.dll")
	procNetUserGetLocalGroups = netapi32.NewProc("NetUserGetLocalGroups")
	procNetApiBufferFree      = netapi32.NewProc("NetApiBufferFree")
	procNetUserAdd            = netapi32.NewProc("NetUserAdd")
	procNetUserSetInfo        = netapi32.NewProc("NetUserSetInfo")
	procNetLocalGroupAdd      = netapi32.NewProc("NetLocalGroupAddMembers")
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUser             = advapi32.NewProc("LogonUserW")
)

func InspectAccount(name string) (AccountStatus, error) {
	user, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return AccountStatus{}, err
	}
	var buffer *byte
	if err := windows.NetUserGetInfo(nil, user, 4, &buffer); err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == 2221 {
			return AccountStatus{}, nil
		}
		return AccountStatus{}, fmt.Errorf("NetUserGetInfo: %w", err)
	}
	defer procNetApiBufferFree.Call(uintptr(unsafe.Pointer(buffer)))
	info := (*userInfo4)(unsafe.Pointer(buffer))
	status := AccountStatus{Exists: true, Enabled: info.Flags&0x2 == 0, LockedOut: info.Flags&0x10 != 0}
	if info.UserSID != nil {
		status.SID = info.UserSID.String()
	}
	groups, err := accountLocalGroups(user)
	if err != nil {
		return AccountStatus{}, err
	}
	administrators, err := builtinGroupName(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return AccountStatus{}, err
	}
	users, err := builtinGroupName(windows.WinBuiltinUsersSid)
	if err != nil {
		return AccountStatus{}, err
	}
	status.Administrator = groups[strings.ToLower(administrators)]
	status.UsersMember = groups[strings.ToLower(users)]
	return status, nil
}

func EnsureAccount(name string, password []byte, reset bool) error {
	defer clear(password)
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	status, err := InspectAccount(name)
	if err != nil {
		return err
	}
	if status.Exists && (status.Administrator || strings.EqualFold(status.SID, current.User.Sid.String())) {
		return fmt.Errorf("refusing to manage an Administrator or the current operator account")
	}
	name16, _ := windows.UTF16FromString(name)
	password16, err := windows.UTF16FromString(string(password))
	if err != nil {
		return err
	}
	defer clear(password16)
	if !status.Exists {
		comment, _ := windows.UTF16PtrFromString("Dedicated low-privilege account for Agent_b")
		info := userInfo1{Name: &name16[0], Password: &password16[0], Priv: 1, Comment: comment, Flags: 0x1 | 0x200 | 0x10000}
		var parameter uint32
		if result, _, _ := procNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&parameter))); result != 0 {
			return fmt.Errorf("NetUserAdd parameter %d: %w", parameter, syscall.Errno(result))
		}
	} else {
		if !reset {
			return fmt.Errorf("account exists and password reset was not authorized")
		}
		info := userInfo1003{Password: &password16[0]}
		var parameter uint32
		if result, _, _ := procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(&name16[0])), 1003, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&parameter))); result != 0 {
			return fmt.Errorf("NetUserSetInfo parameter %d: %w", parameter, syscall.Errno(result))
		}
	}
	users, err := builtinGroupName(windows.WinBuiltinUsersSid)
	if err != nil {
		return err
	}
	qualified, _ := windows.UTF16PtrFromString(`.\` + name)
	group, _ := windows.UTF16PtrFromString(users)
	member := localGroupMembersInfo3{DomainAndName: qualified}
	if result, _, _ := procNetLocalGroupAdd.Call(0, uintptr(unsafe.Pointer(group)), 3, uintptr(unsafe.Pointer(&member)), 1); result != 0 && result != 1378 {
		return fmt.Errorf("NetLocalGroupAddMembers: %w", syscall.Errno(result))
	}
	var token windows.Handle
	domain, _ := windows.UTF16PtrFromString(".")
	result, _, callErr := procLogonUser.Call(uintptr(unsafe.Pointer(&name16[0])), uintptr(unsafe.Pointer(domain)), uintptr(unsafe.Pointer(&password16[0])), 2, 0, uintptr(unsafe.Pointer(&token)))
	if result == 0 {
		return fmt.Errorf("LogonUser: %w", callErr)
	}
	return windows.CloseHandle(token)
}

func accountLocalGroups(user *uint16) (map[string]bool, error) {
	var buffer uintptr
	var read, total uint32
	result, _, _ := procNetUserGetLocalGroups.Call(0, uintptr(unsafe.Pointer(user)), 0, 1, uintptr(unsafe.Pointer(&buffer)), ^uintptr(0), uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)))
	if result != 0 {
		return nil, fmt.Errorf("NetUserGetLocalGroups: %w", syscall.Errno(result))
	}
	defer procNetApiBufferFree.Call(buffer)
	groups := map[string]bool{}
	for _, item := range unsafe.Slice((*localGroupUsersInfo0)(unsafe.Pointer(buffer)), read) {
		groups[strings.ToLower(windows.UTF16PtrToString(item.Name))] = true
	}
	return groups, nil
}

func builtinGroupName(kind windows.WELL_KNOWN_SID_TYPE) (string, error) {
	sid, err := windows.CreateWellKnownSid(kind)
	if err != nil {
		return "", err
	}
	name, _, _, err := sid.LookupAccount("")
	return name, err
}
