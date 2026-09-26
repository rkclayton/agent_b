//go:build windows

package credential

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	cryptProtectUIForbidden = 0x1
	// Item 2li: CRYPTPROTECT_LOCAL_MACHINE. rel-1.15.0/W0 measured what this
	// costs and what it does not: the blob names a master key that is not in any
	// user's key store, so anything running on the machine can decrypt it -- the
	// ACL is what protects it -- and CryptUnprotectData needs no flag to read it
	// back, because a DPAPI blob is self-describing. Only the write side changes.
	cryptProtectLocalMachine = 0x4
)

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

type dataBlob struct {
	size uint32
	data *byte
}

func protect(plain []byte) ([]byte, error) {
	return cryptData(procCryptProtectData, plain, cryptProtectUIForbidden)
}

// protectMachine is reachable only from the service-account store: see
// Store.WriteMachine, which is the single caller, and NewNamed, which never
// builds a store that has one.
func protectMachine(plain []byte) ([]byte, error) {
	return cryptData(procCryptProtectData, plain, cryptProtectUIForbidden|cryptProtectLocalMachine)
}

func unprotect(protected []byte) ([]byte, error) {
	return cryptData(procCryptUnprotectData, protected, cryptProtectUIForbidden)
}

func cryptData(proc *syscall.LazyProc, input []byte, flags uintptr) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("DPAPI input is empty")
	}
	in := dataBlob{size: uint32(len(input)), data: &input[0]}
	var out dataBlob
	result, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		flags,
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(input)
	if result == 0 {
		return nil, fmt.Errorf("DPAPI call failed: %w", callErr)
	}
	if out.data == nil || out.size == 0 {
		return nil, fmt.Errorf("DPAPI returned an empty result")
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return append([]byte(nil), unsafe.Slice(out.data, out.size)...), nil
}
