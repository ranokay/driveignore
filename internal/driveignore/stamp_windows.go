//go:build windows

package driveignore

import (
	"golang.org/x/sys/windows"
)

// dirChangeStamp always reports unknown on Windows. NTFS updates directory
// timestamps lazily, so a freshly changed directory can keep the same change
// time for a long time (a Windows CI runner reproduced this). A stamp-based
// prune would then skip changed subtrees, so Windows always walks instead.
func dirChangeStamp(string) (int64, error) {
	return 0, nil
}

// fileInode returns the file's volume-unique index, the journal's anchor for
// deletion proofs. Zero means unknown: callers must route to creation, never
// deletion.
func fileInode(path string) (uint64, error) {
	handle, err := openReadAttributes(path)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return 0, err
	}
	return uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), nil
}

// openReadAttributes opens a handle that works for both files and
// directories, which needs FILE_FLAG_BACKUP_SEMANTICS.
func openReadAttributes(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}
