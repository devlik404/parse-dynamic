package securefile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenReadAndAppendRejectFinalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link creation normally requires elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := OpenRead(link); err == nil {
		t.Fatal("OpenRead followed a symbolic link")
	}
	if _, err := OpenAppend(link); err == nil {
		t.Fatal("OpenAppend followed a symbolic link")
	}
}

func TestOpenAppendForcesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	file, err := OpenAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}
