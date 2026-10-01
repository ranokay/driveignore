package version

import "testing"

func TestFormat(t *testing.T) {
	tests := []struct {
		name, v, c, d, want string
	}{
		{"full", "1.2.0", "abcdef1234567890", "2026-10-01", "1.2.0 (abcdef1, 2026-10-01)"},
		{"commit only", "1.2.0", "abcdef1", "", "1.2.0 (abcdef1)"},
		{"bare", "1.2.0", "", "", "1.2.0"},
		{"devel with commit", "devel", "abc", "", "devel (abc)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := format(tt.v, tt.c, tt.d); got != tt.want {
				t.Fatalf("format() = %q, want %q", got, tt.want)
			}
		})
	}
}
