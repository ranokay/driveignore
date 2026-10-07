//go:build !darwin

package driveignore

import "errors"

// errIgnoreAttrUnsupported reports that the sealing mechanism does not exist on
// this platform. The guard command refuses before reaching a pass, so this is a
// defensive error for direct core use.
var errIgnoreAttrUnsupported = errors.New("the File Provider ignore attribute is only available on macOS")

func setIgnoreAttr(string) error { return errIgnoreAttrUnsupported }

func removeIgnoreAttr(string) error { return errIgnoreAttrUnsupported }

func hasIgnoreAttr(string) (bool, error) { return false, errIgnoreAttrUnsupported }
