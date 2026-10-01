package driveignore

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func buildBenchTree(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	for d := range 50 {
		dir := filepath.Join(root, fmt.Sprintf("dir-%02d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for f := range 100 {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%03d.txt", f)), nil, 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	return root
}

func BenchmarkWalk(b *testing.B) {
	root := buildBenchTree(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, func(string, fs.DirEntry, string) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWalkFilepathWalk measures the walker the core replaced: filepath.Walk
// plus an extra os.Stat per entry, which was used to mark directories.
func BenchmarkWalkFilepathWalk(b *testing.B) {
	root := buildBenchTree(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			_, err = os.Stat(path)
			return err
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
