package piauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreModifyPreservesFieldsAndMode(t *testing.T) {
	path := writeAuth(t, map[string]any{
		"other": map[string]any{"secret": "keep"},
		"xai":   map[string]any{"type": "oauth", "access": "old", "refresh": "refresh", "expires": 1, "unknown": true},
	})
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Modify(context.Background(), "xai", func(_ context.Context, current OAuthCredential) (*OAuthCredential, error) {
		current.Access = "new"
		current.Expires = 2
		return &current, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Access != "new" {
		t.Fatalf("access = %q", got.Access)
	}
	root := readMap(t, path)
	if root["other"].(map[string]any)["secret"] != "keep" || root["xai"].(map[string]any)["unknown"] != true {
		t.Fatalf("fields changed: %#v", root)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestStoreRejectsOversizedAuthFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxAuthFileBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "xai"); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreCanonicalizesSymlink(t *testing.T) {
	target := writeAuth(t, map[string]any{"xai": map[string]any{"type": "oauth", "access": "old"}})
	link := filepath.Join(t.TempDir(), "auth.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	store, err := New(link)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if store.Path != wantPath {
		t.Fatalf("path = %q, want %q", store.Path, wantPath)
	}
	_, err = store.Modify(context.Background(), "xai", func(_ context.Context, current OAuthCredential) (*OAuthCredential, error) {
		current.Access = "new"
		return &current, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, err)
	}
}

func TestAuthLockRecoversStaleDirectory(t *testing.T) {
	path := writeAuth(t, map[string]any{})
	lockPath := path + ".lock"
	if err := os.Mkdir(lockPath, 0700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-lockStaleAfter - time.Second)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withAuthLock(context.Background(), path, func(context.Context, *authFileLock) error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("callback not called")
	}
}

func TestAuthLockSerializesWriters(t *testing.T) {
	path := writeAuth(t, map[string]any{})
	entered, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := withAuthLock(context.Background(), path, func(context.Context, *authFileLock) error { close(entered); <-release; return nil }); err != nil {
			t.Errorf("first lock: %v", err)
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := withAuthLock(ctx, path, func(context.Context, *authFileLock) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("error = %v", err)
	}
	close(release)
	wg.Wait()
}

func TestModifyDoesNotWriteOrDeleteReplacementLock(t *testing.T) {
	path := writeAuth(t, map[string]any{"xai": map[string]any{"type": "oauth", "access": "old"}})
	store, _ := New(path)
	_, err := store.Modify(context.Background(), "xai", func(_ context.Context, current OAuthCredential) (*OAuthCredential, error) {
		if err := os.Remove(path + ".lock"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path+".lock", 0700); err != nil {
			t.Fatal(err)
		}
		current.Access = "new"
		return &current, nil
	})
	if !errors.Is(err, ErrLockCompromised) {
		t.Fatalf("error = %v", err)
	}
	if got := readMap(t, path)["xai"].(map[string]any)["access"]; got != "old" {
		t.Fatalf("wrote after compromise: %v", got)
	}
	if info, statErr := os.Stat(path + ".lock"); statErr != nil || !info.IsDir() {
		t.Fatalf("successor deleted: %v", statErr)
	}
}

func writeAuth(t *testing.T, value map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	data, _ := json.Marshal(value)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
