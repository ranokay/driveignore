//go:build darwin

package driveignore

import (
	"errors"

	"golang.org/x/sys/unix"
)

// ignoreAttributeName is the File Provider attribute Google Drive for desktop
// honors as "keep this item, and everything below it, out of sync". Only the
// protected "#P" variant has that effect; the plain name does not (verified
// against Drive for desktop 132.0.0.0). Stamping a path whose cloud copy
// already exists moves that copy to the trash.
const ignoreAttributeName = "com.apple.fileprovider.ignore#P"

// ignoreAttributeValue is the value the manual verification used, so the code
// writes exactly the form whose behavior was measured.
var ignoreAttributeValue = []byte("1")

func setIgnoreAttr(path string) error {
	return unix.Setxattr(path, ignoreAttributeName, ignoreAttributeValue, 0)
}

func removeIgnoreAttr(path string) error {
	return unix.Removexattr(path, ignoreAttributeName)
}

func hasIgnoreAttr(path string) (bool, error) {
	// The buffer only needs to be big enough for the value the guard writes;
	// ERANGE still means the attribute exists, which is all the caller needs.
	buf := make([]byte, len(ignoreAttributeValue))
	if _, err := unix.Getxattr(path, ignoreAttributeName, buf); err != nil {
		if errors.Is(err, unix.ENOATTR) {
			return false, nil
		}
		if errors.Is(err, unix.ERANGE) {
			return true, nil
		}
		return false, err
	}
	return true, nil
}
