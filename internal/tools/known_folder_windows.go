package tools

import "golang.org/x/sys/windows"

func init() { knownFolderPath = windowsKnownFolderPath }

func windowsKnownFolderPath(name string) (string, bool) {
	folders := map[string]*windows.KNOWNFOLDERID{
		"downloads": windows.FOLDERID_Downloads,
		"documents": windows.FOLDERID_Documents,
		"desktop":   windows.FOLDERID_Desktop,
		"pictures":  windows.FOLDERID_Pictures,
	}
	path, err := windows.KnownFolderPath(folders[name], 0)
	if err != nil {
		return "", false
	}
	return path, true
}
