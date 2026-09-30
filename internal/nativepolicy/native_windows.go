//go:build windows

package nativepolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"harness/internal/credential"
)

type ACLRequest struct{ Application, Data, Workspace, Exchange, SID string }
type Drift struct{ Subject, Expected, Found string }
type aclTarget struct {
	path, intent string
	rights       uint32
	inheritance  uint8
	deny         bool
}

func ValidateACLPolicy(request ACLRequest) error {
	_, err := aclTargets(request, false)
	return err
}

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

func SetManagedACLRule(path, sidText string, rights uint32, inheritance uint32, deny bool) error {
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return err
	}
	mode := windows.ACCESS_MODE(windows.SET_ACCESS)
	if deny {
		mode = windows.ACCESS_MODE(windows.DENY_ACCESS)
	}
	return mergeACL(path, windows.EXPLICIT_ACCESS{AccessPermissions: windows.ACCESS_MASK(rights), AccessMode: mode, Inheritance: inheritance,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
}

func RemoveManagedACLRules(path, sidText string) error {
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return err
	}
	return mergeACL(path, windows.EXPLICIT_ACCESS{AccessMode: windows.REVOKE_ACCESS,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
}

func mergeACL(path string, entry windows.EXPLICIT_ACCESS) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	merged, err := windows.BuildSecurityDescriptor(nil, nil, []windows.EXPLICIT_ACCESS{entry}, nil, descriptor)
	if err != nil {
		return err
	}
	dacl, _, err := merged.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func HasManagedACLRule(path, sidText string, rights uint32, inheritance uint8, deny bool) (bool, error) {
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return false, err
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return false, err
	}
	wantType := uint8(windows.ACCESS_ALLOWED_ACE_TYPE)
	if deny {
		wantType = windows.ACCESS_DENIED_ACE_TYPE
	}
	matches := 0
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return false, err
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType == wantType && ace.Header.AceFlags&0x13 == inheritance && ace.Mask == windows.ACCESS_MASK(rights) && aceSID.Equals(sid) {
			matches++
		}
	}
	return matches == 1, nil
}

func InspectACLPolicy(request ACLRequest) ([]Drift, error) {
	targets, err := aclTargets(request, false)
	if err != nil {
		return nil, err
	}
	var drift []Drift
	for _, target := range targets {
		if _, err := os.Stat(target.path); err != nil {
			if os.IsNotExist(err) {
				drift = append(drift, Drift{target.path, target.intent, "path missing"})
				continue
			}
			return nil, err
		}
		ok, err := HasManagedACLRule(target.path, request.SID, target.rights, target.inheritance, target.deny)
		if err != nil {
			return nil, err
		}
		if !ok {
			drift = append(drift, Drift{target.path, target.intent, "managed rule missing or different"})
		}
	}
	return drift, nil
}

func ApplyACLPolicy(request ACLRequest, remove bool) error {
	targets, err := aclTargets(request, !remove)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := os.Stat(target.path); err != nil {
			if remove && os.IsNotExist(err) {
				continue
			}
			return err
		}
		if remove {
			err = RemoveManagedACLRules(target.path, request.SID)
		} else {
			err = SetManagedACLRule(target.path, request.SID, target.rights, uint32(target.inheritance), target.deny)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", target.path, err)
		}
	}
	return nil
}

func aclTargets(request ACLRequest, create bool) ([]aclTarget, error) {
	clean := func(path string) string {
		absolute, _ := filepath.Abs(os.ExpandEnv(path))
		return filepath.Clean(absolute)
	}
	request.Application, request.Data, request.Workspace, request.Exchange = clean(request.Application), clean(request.Data), clean(request.Workspace), clean(request.Exchange)
	for _, path := range []string{request.Application, request.Data} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("required directory does not exist: %s", path)
		}
	}
	profiles, _ := os.ReadDir(filepath.Join(request.Data, "profiles"))
	plans, scratch := []string{}, []string{}
	for _, profile := range profiles {
		if profile.IsDir() {
			plans = append(plans, filepath.Join(request.Data, "profiles", profile.Name(), "plans"))
			scratch = append(scratch, filepath.Join(request.Data, "profiles", profile.Name(), "scratch"))
		}
	}
	basePlans, baseScratch := filepath.Join(request.Data, "plans"), filepath.Join(request.Data, "scratch")
	if len(plans) == 0 {
		plans = append(plans, basePlans)
	} else if _, err := os.Stat(basePlans); err == nil {
		plans = append([]string{basePlans}, plans...)
	}
	if len(scratch) == 0 {
		scratch = append(scratch, baseScratch)
	} else if _, err := os.Stat(baseScratch); err == nil {
		scratch = append([]string{baseScratch}, scratch...)
	}
	workspaceScratch := false
	for _, path := range scratch {
		if sameOrWithin(request.Workspace, path) {
			workspaceScratch = true
		}
	}
	trees := []string{request.Application, request.Data, request.Exchange}
	if !workspaceScratch {
		trees = append(trees, request.Workspace)
	}
	for i := range trees {
		for j := i + 1; j < len(trees); j++ {
			if sameOrWithin(trees[i], trees[j]) || sameOrWithin(trees[j], trees[i]) {
				return nil, fmt.Errorf("application, operator-data, workspace, and exchange directories must be disjoint trees")
			}
		}
	}
	if create {
		for _, path := range append(append(plans, scratch...), request.Exchange) {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return nil, err
			}
		}
	}
	reachable := append([]string{request.Application, request.Workspace, request.Exchange}, append(plans, scratch...)...)
	seen, targets := map[string]bool{}, []aclTarget{}
	programFiles, programData := clean(os.Getenv("ProgramFiles")), clean(os.Getenv("ProgramData"))
	for _, path := range reachable {
		for parent := filepath.Dir(path); filepath.Dir(parent) != parent && filepath.Dir(filepath.Dir(parent)) != filepath.Dir(parent); parent = filepath.Dir(parent) {
			key := strings.ToLower(parent)
			if parent != programFiles && parent != programData && !seen[key] {
				seen[key] = true
				targets = append(targets, aclTarget{parent, "grant parent-directory traverse", 0x20, 0, false})
			}
		}
	}
	targets = append(targets, aclTarget{request.Application, "deny application-tree mutation", 0xd0156, 3, true}, aclTarget{request.Application, "grant application-tree read and execute", 0x200a9, 3, false}, aclTarget{request.Data, "deny service identity access to operator data except traversal", 0x1f01df, 3, true})
	for _, path := range plans {
		targets = append(targets, aclTarget{path, "grant plans-folder Modify", 0x301bf, 3, false})
	}
	for _, path := range scratch {
		targets = append(targets, aclTarget{path, "grant scratch-folder Modify", 0x301bf, 3, false})
	}
	if !workspaceScratch {
		if _, err := os.Stat(request.Workspace); err == nil {
			targets = append(targets, aclTarget{request.Workspace, "grant legacy workspace Modify", 0x301bf, 3, false})
		}
	}
	targets = append(targets, aclTarget{request.Exchange, "grant exchange-folder Modify", 0x301bf, 3, false})
	return targets, nil
}

func sameOrWithin(child, parent string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

func ExecuteHelper(request HelperRequest) HelperResult {
	sha, err := SelfSHA256()
	if err != nil || !strings.EqualFold(sha, request.ExpectedSHA) {
		return HelperResult{Message: "elevated helper identity does not match the verified executable"}
	}
	steps := []string{}
	if request.Operation == "provision" {
		password, readErr := credential.New(filepath.Dir(request.CredentialPath)).Read()
		if readErr != nil {
			return HelperResult{Message: "read service credential: " + readErr.Error()}
		}
		if err := EnsureAccount(request.Account, password, request.Reset); err != nil {
			return HelperResult{Message: err.Error(), Steps: steps}
		}
		steps = append(steps, "account — PASS")
	}
	account, err := InspectAccount(request.Account)
	if err != nil || !account.Exists {
		return HelperResult{Message: "service account is missing", Steps: steps}
	}
	request.ACL.SID, request.Firewall.SID = account.SID, account.SID
	remove := request.Action == "remove"
	if err := ApplyACLPolicy(request.ACL, remove); err != nil {
		return HelperResult{Message: "ACL policy: " + err.Error(), Steps: steps}
	}
	if !remove {
		if drift, err := InspectACLPolicy(request.ACL); err != nil || len(drift) > 0 {
			return HelperResult{Message: firstDrift("ACL", drift, err), Steps: steps}
		}
	}
	steps = append(steps, "protections — PASS")
	if err := ApplyFirewallPolicy(request.Firewall, remove); err != nil {
		return HelperResult{Message: "firewall policy: " + err.Error(), Steps: steps}
	}
	if !remove {
		if drift, err := InspectFirewallPolicy(request.Firewall); err != nil || len(drift) > 0 {
			return HelperResult{Message: firstDrift("firewall", drift, err), Steps: steps}
		}
	}
	steps = append(steps, "network — PASS")
	return HelperResult{OK: true, Message: "service identity account, protections, and network policy verified", Steps: steps}
}

func firstDrift(component string, drift []Drift, err error) string {
	if err != nil {
		return component + " verification: " + err.Error()
	}
	if len(drift) == 0 {
		return component + " verification failed"
	}
	return fmt.Sprintf("DRIFT %s: expected %s got %s", drift[0].Subject, drift[0].Expected, drift[0].Found)
}

func SelfSHA256() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func WriteHelperRequest(path string, request *HelperRequest) error {
	sha, err := SelfSHA256()
	if err != nil {
		return err
	}
	request.ExpectedSHA = sha
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func LaunchElevatedHelper(ctx context.Context, requestPath, resultPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := verifyAuthenticode(executable); err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(executable)
	parameters, _ := windows.UTF16PtrFromString(`--service-helper "` + strings.ReplaceAll(requestPath, `"`, `\"`) + `" --service-result "` + strings.ReplaceAll(resultPath, `"`, `\"`) + `"`)
	if err := windows.ShellExecute(0, verb, file, parameters, nil, 0); err != nil {
		return fmt.Errorf("Windows elevation was declined or could not be started: %w", err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(resultPath); err == nil {
				return nil
			}
		}
	}
}

func verifyAuthenticode(path string) error {
	type fileInfo struct {
		Size    uint32
		Path    *uint16
		File    windows.Handle
		Subject *windows.GUID
	}
	type trustData struct {
		Size                              uint32
		Policy, Client                    uintptr
		UIChoice, Revocation, UnionChoice uint32
		Info                              uintptr
		StateAction                       uint32
		State                             windows.Handle
		URL                               uintptr
		ProviderFlags, UIContext          uint32
	}
	wide, _ := windows.UTF16PtrFromString(path)
	info := fileInfo{Size: uint32(unsafe.Sizeof(fileInfo{})), Path: wide}
	data := trustData{Size: uint32(unsafe.Sizeof(trustData{})), UIChoice: 2, UnionChoice: 1, Info: uintptr(unsafe.Pointer(&info)), ProviderFlags: 0x1000}
	action := windows.GUID{Data1: 0x00aac56b, Data2: 0xcd44, Data3: 0x11d0, Data4: [8]byte{0x8c, 0xc2, 0, 0xc0, 0x4f, 0xc2, 0x95, 0xee}}
	result, _, _ := windows.NewLazySystemDLL("wintrust.dll").NewProc("WinVerifyTrust").Call(^uintptr(0), uintptr(unsafe.Pointer(&action)), uintptr(unsafe.Pointer(&data)))
	if int32(result) != 0 {
		return fmt.Errorf("elevated helper Authenticode verification failed: 0x%x", result)
	}
	return nil
}
