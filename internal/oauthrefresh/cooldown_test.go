package oauthrefresh

import (
	"errors"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func TestFailureCache(t *testing.T) {
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	cache := NewFailureCache(30 * time.Minute)
	dead := &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: "codex", Err: errors.New("invalid_state")}
	cache.Remember("refresh-1", now, dead)

	if err := cache.Check("refresh-1", now.Add(time.Minute)); err == nil {
		t.Fatal("expected cooldown hit")
	}
	if err := cache.Check("refresh-2", now.Add(time.Minute)); err != nil {
		t.Fatalf("other token = %v", err)
	}
	if err := cache.Check("refresh-1", now.Add(31*time.Minute)); err != nil {
		t.Fatalf("after cooldown = %v", err)
	}

	cache.Remember("refresh-1", now, dead)
	cache.Forget("refresh-1")
	if err := cache.Check("refresh-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("after forget = %v", err)
	}

	cache.Remember("refresh-1", now, &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: "codex"})
	if err := cache.Check("refresh-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("rate limit should not cool down: %v", err)
	}
}

func TestNilFailureCacheIsNoop(t *testing.T) {
	var cache *FailureCache
	if err := cache.Check("token", time.Now()); err != nil {
		t.Fatal(err)
	}
	cache.Remember("token", time.Now(), &usageprovider.Error{Code: usageprovider.ErrAuthExpired})
	cache.Forget("token")
}
