package driveignore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirChangeStampAdvancesOnDirectoryChange(t *testing.T) {
	dir := t.TempDir()
	before, err := dirChangeStamp(dir)
	require.NoError(t, err)
	if before == 0 {
		t.Skip("change stamps are unavailable on this platform")
	}

	write(t, filepath.Join(dir, "new.txt"), "x")

	after, err := dirChangeStamp(dir)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}

func TestDirChangeStampMissingPathErrors(t *testing.T) {
	probe, err := dirChangeStamp(t.TempDir())
	require.NoError(t, err)
	if probe == 0 {
		t.Skip("change stamps are unavailable on this platform")
	}

	stamp, err := dirChangeStamp(filepath.Join(t.TempDir(), "gone"))
	require.Error(t, err)
	require.Zero(t, stamp)
}

func TestFileInodeDistinguishesLinksFromCopies(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.txt")
	copied := filepath.Join(dir, "copied.txt")
	linked := filepath.Join(dir, "linked.txt")
	write(t, original, "x")
	write(t, copied, "x")
	require.NoError(t, os.Link(original, linked))

	originalInode, err := fileInode(original)
	require.NoError(t, err)
	if originalInode == 0 {
		t.Skip("inodes are unavailable on this platform")
	}
	linkedInode, err := fileInode(linked)
	require.NoError(t, err)
	copiedInode, err := fileInode(copied)
	require.NoError(t, err)

	require.Equal(t, originalInode, linkedInode, "hardlinks share an inode")
	require.NotEqual(t, originalInode, copiedInode)
}
