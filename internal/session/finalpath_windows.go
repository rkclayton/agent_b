//go:build windows

package session

import (
	"strings"
	"syscall"
	"unsafe"
)

var procGetFinalPathNameByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

// finalPath is the folder a path actually names, with junctions and links
// resolved by the file system itself; filepath.EvalSymlinks does not follow
// junctions since Go 1.23.
func finalPath(path string) (string, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(handle)
	buffer := make([]uint16, 512)
	for {
		n, _, callErr := procGetFinalPathNameByHandle.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		if n == 0 {
			return "", callErr
		}
		if int(n) < len(buffer) {
			value := syscall.UTF16ToString(buffer[:n])
			if strings.HasPrefix(value, `\\?\UNC\`) {
				return `\\` + value[len(`\\?\UNC\`):], nil
			}
			return strings.TrimPrefix(value, `\\?\`), nil
		}
		buffer = make([]uint16, n)
	}
}
