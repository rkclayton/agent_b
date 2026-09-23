//go:build windows

package web

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	afInet                         = 2
	tcpTableOwnerPIDAll            = 5
	mibTCPStateEstablished         = 5
	processQueryLimitedInformation = 0x1000
)

type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

var (
	iphlpapi                 = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTCPTable  = iphlpapi.NewProc("GetExtendedTcpTable")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procProcessIDToSessionID = kernel32.NewProc("ProcessIdToSessionId")
)

func requireOperatorHTTPClient(r *http.Request) error {
	remote, err := tcpRequestAddress(r.RemoteAddr)
	if err != nil {
		return fmt.Errorf("resolve HTTP client address: %w", err)
	}
	localValue := r.Context().Value(http.LocalAddrContextKey)
	localAddress, ok := localValue.(net.Addr)
	if !ok {
		return fmt.Errorf("HTTP server address is unavailable")
	}
	local, err := tcpRequestAddress(localAddress.String())
	if err != nil {
		return fmt.Errorf("resolve HTTP server address: %w", err)
	}
	if !remote.IP.IsLoopback() || !local.IP.IsLoopback() {
		return fmt.Errorf("operator-context request was not loopback")
	}
	pid, err := loopbackTCPClientPID(remote, local)
	if err != nil {
		return err
	}
	descendant, err := processDescendsFrom(pid, uint32(syscall.Getpid()))
	if err != nil {
		return fmt.Errorf("inspect HTTP client process ancestry: %w", err)
	}
	if descendant {
		return fmt.Errorf("HTTP client process %d is Agent_b or one of its descendants", pid)
	}
	clientSID, err := processUserSID(pid)
	if err != nil {
		return fmt.Errorf("read HTTP client process identity: %w", err)
	}
	operatorSID, err := currentProcessUserSID()
	if err != nil {
		return fmt.Errorf("read Agent_b process identity: %w", err)
	}
	clientSession, err := processSessionID(pid)
	if err != nil {
		return fmt.Errorf("read HTTP client interactive session: %w", err)
	}
	operatorSession, err := processSessionID(uint32(syscall.Getpid()))
	if err != nil {
		return fmt.Errorf("read Agent_b interactive session: %w", err)
	}
	if !operatorIdentityAllowed(clientSID, operatorSID, clientSession, operatorSession) {
		return fmt.Errorf("HTTP client account %s is neither the Agent_b account %s nor in its interactive session %d", accountLabel(clientSID), accountLabel(operatorSID), operatorSession)
	}
	return nil
}

func operatorIdentityAllowed(clientSID, operatorSID string, clientSession, operatorSession uint32) bool {
	return clientSID == operatorSID || clientSession == operatorSession
}

func processSessionID(pid uint32) (uint32, error) {
	var session uint32
	result, _, callErr := procProcessIDToSessionID.Call(uintptr(pid), uintptr(unsafe.Pointer(&session)))
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return 0, callErr
	}
	return session, nil
}

func accountLabel(sidText string) string {
	sid, err := syscall.StringToSid(sidText)
	if err != nil {
		return sidText
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil || account == "" {
		return sidText
	}
	if domain != "" {
		account = domain + `\` + account
	}
	return fmt.Sprintf("%s (%s)", account, sidText)
}

func processDescendsFrom(pid, ancestor uint32) (bool, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(snapshot)
	parents := map[uint32]uint32{}
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return false, err
	}
	for {
		parents[entry.ProcessID] = entry.ParentProcessID
		if err := syscall.Process32Next(snapshot, &entry); err != nil {
			if err == syscall.ERROR_NO_MORE_FILES {
				break
			}
			return false, err
		}
	}
	seen := map[uint32]bool{}
	for pid != 0 && !seen[pid] {
		if pid == ancestor {
			return true, nil
		}
		seen[pid] = true
		parent, present := parents[pid]
		if !present || parent == pid {
			break
		}
		pid = parent
	}
	return false, nil
}

func tcpRequestAddress(value string) (*net.TCPAddr, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return nil, fmt.Errorf("IPv4 address required")
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}

func loopbackTCPClientPID(client, server *net.TCPAddr) (uint32, error) {
	var size uint32
	result, _, callErr := procGetExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0)
	if result != uintptr(syscall.ERROR_INSUFFICIENT_BUFFER) || size < 4 {
		return 0, fmt.Errorf("size Windows TCP owner table: %w", callErr)
	}
	buffer := make([]byte, size)
	result, _, callErr = procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0)
	if result != 0 {
		return 0, fmt.Errorf("read Windows TCP owner table: %w", callErr)
	}
	count := *(*uint32)(unsafe.Pointer(&buffer[0]))
	rowSize := unsafe.Sizeof(mibTCPRowOwnerPID{})
	for index := uint32(0); index < count; index++ {
		offset := uintptr(4) + uintptr(index)*rowSize
		if offset+rowSize > uintptr(len(buffer)) {
			break
		}
		row := (*mibTCPRowOwnerPID)(unsafe.Pointer(&buffer[offset]))
		if row.State == mibTCPStateEstablished &&
			windowsTCPPort(row.LocalPort) == client.Port && windowsTCPPort(row.RemotePort) == server.Port &&
			windowsIPv4(row.LocalAddr).Equal(client.IP) && windowsIPv4(row.RemoteAddr).Equal(server.IP) {
			return row.OwningPID, nil
		}
	}
	return 0, fmt.Errorf("HTTP client process was not found in the Windows TCP owner table")
}

func windowsTCPPort(value uint32) int {
	bytes := *(*[4]byte)(unsafe.Pointer(&value))
	return int(binary.BigEndian.Uint16(bytes[:2]))
}

func windowsIPv4(value uint32) net.IP {
	bytes := *(*[4]byte)(unsafe.Pointer(&value))
	return net.IPv4(bytes[0], bytes[1], bytes[2], bytes[3])
}

func currentProcessUserSID() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	return tokenUserSID(token)
}

func processUserSID(pid uint32) (string, error) {
	process, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(process)
	var token syscall.Token
	if err := syscall.OpenProcessToken(process, syscall.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer token.Close()
	return tokenUserSID(token)
}

func tokenUserSID(token syscall.Token) (string, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String()
}
