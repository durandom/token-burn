package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAssetName(t *testing.T) {
	got, err := assetName("v0.1.0", "darwin", "arm64")
	if err != nil {
		t.Fatalf("assetName() error = %v", err)
	}
	want := "token-burn_v0.1.0_darwin_arm64.tar.gz"
	if got != want {
		t.Fatalf("assetName() = %q, want %q", got, want)
	}
}

func TestAssetNameRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := assetName("v0.1.0", "windows", "amd64"); err == nil {
		t.Fatal("assetName() error = nil, want error")
	}
	if _, err := assetName("v0.1.0", "linux", "386"); err == nil {
		t.Fatal("assetName() error = nil, want error")
	}
}

func TestReplaceBinaryCrossDeviceFallsBackToCopy(t *testing.T) {
	orig := renameFile
	renameFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	defer func() { renameFile = orig }()

	dir := t.TempDir()
	source := filepath.Join(dir, "token-burn.new")
	dest := filepath.Join(dir, "bin", "token-burn")
	if err := os.WriteFile(source, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0750); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(source, dest); err != nil {
		t.Fatalf("replaceBinary() error = %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("dest content = %q, want %q", got, "new")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0750 {
		t.Fatalf("dest mode = %v, want 0750", info.Mode().Perm())
	}
	if _, err := os.Stat(dest + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup not cleaned up: %v", err)
	}
}

func TestReplaceBinaryRestoresBackupOnFailure(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "does-not-exist")
	dest := filepath.Join(dir, "token-burn")
	if err := os.WriteFile(dest, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(source, dest); err == nil {
		t.Fatal("replaceBinary() error = nil, want error")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("existing binary lost after failed upgrade: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("dest content = %q, want %q", got, "old")
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := map[string]string{
		"v0.1.0": "0.1.0",
		"0.1.0":  "0.1.0",
		"dev":    "dev",
	}
	for raw, want := range tests {
		if got := normalizeVersion(raw); got != want {
			t.Fatalf("normalizeVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}
