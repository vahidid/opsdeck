//go:build windows

package paths

import "golang.org/x/sys/windows"

func downloadsKnownFolder() string {
	d, _ := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0)
	return d
}
