//go:build !darwin && !linux && !windows

package driveignore

// dirChangeStamp returns 0 on platforms without a change stamp: unknown means
// the scan always walks instead of pruning.
func dirChangeStamp(string) (int64, error) { return 0, nil }

// fileInode returns 0 on platforms without inode identity: no anchor means
// deletion proofs must route to creation instead.
func fileInode(string) (uint64, error) { return 0, nil }
