package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileExistsNonEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	nonEmpty := filepath.Join(dir, "non-empty.bin")
	if err := os.WriteFile(nonEmpty, []byte("x"), 0o644); err != nil {
		t.Fatalf("write non-empty file failed: %v", err)
	}
	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty file failed: %v", err)
	}
	missing := filepath.Join(dir, "missing.bin")
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "non-empty file", path: nonEmpty, want: true},
		{name: "empty file", path: empty, want: false},
		{name: "missing file", path: missing, want: false},
		{name: "directory", path: subDir, want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := fileExistsNonEmpty(tt.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("path=%s got=%v want=%v", tt.path, got, tt.want)
			}
		})
	}
}
