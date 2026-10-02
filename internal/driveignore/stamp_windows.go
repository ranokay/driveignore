//go:build windows

package driveignore

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileBasicInfo mirrors the Windows FILE_BASIC_INFO layout, which is where
// the change time lives; x/sys exposes the information class but not the
// struct. The trailing padding matches the C compiler's alignment.
type fileBasicInfo struct {
	CreationTime   windows.Filetime
	LastAccessTime windows.Filetime
	LastWriteTime  windows.Filetime
	ChangeTime     windows.Filetime
	FileAttributes uint32
	_              uint32
}

// dirChangeStamp returns the directory's change time in unix nanoseconds: the
// Windows analogue of ctime. A zero return means unknown, which forces the
// scan to walk the subtree.
func dirChangeStamp(path string) (int64, error) {
	handle, err := openReadAttributes(path)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)

	var info fileBasicInfo
	err = windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return 0, err
	}
	return info.ChangeTime.Nanoseconds(), nil
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
