package oauthrefresh

import (
	"errors"
	"sync"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const DefaultFailureCooldown = 30 * time.Minute

type failureItem struct {
	until time.Time
	err   error
}

// FailureCache remembers a dead refresh grant so the same token is not POSTed
// in a tight poll loop.
type FailureCache struct {
	cooldown time.Duration
	mu       sync.Mutex
	items    map[string]failureItem
}

func NewFailureCache(cooldown time.Duration) *FailureCache {
	if cooldown <= 0 {
		cooldown = DefaultFailureCooldown
	}
	return &FailureCache{cooldown: cooldown, items: map[string]failureItem{}}
}

// Check returns the remembered error while the cooldown for token is active.
func (c *FailureCache) Check(token string, now time.Time) error {
	if c == nil || token == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[token]
	if !ok || !now.Before(item.until) {
		if ok {
			delete(c.items, token)
		}
		return nil
	}
	return item.err
}

// Remember stores err for token when it means the grant is dead.
func (c *FailureCache) Remember(token string, now time.Time, err error) {
	if c == nil || token == "" || !authExpired(err) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]failureItem{}
	}
	c.items[token] = failureItem{until: now.Add(c.cooldownOrDefault()), err: err}
}

func (c *FailureCache) cooldownOrDefault() time.Duration {
	if c == nil || c.cooldown <= 0 {
		return DefaultFailureCooldown
	}
	return c.cooldown
}

// Forget clears a token after a successful refresh or rotation.
func (c *FailureCache) Forget(token string) {
	if c == nil || token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, token)
}

func authExpired(err error) bool {
	var providerErr *usageprovider.Error
	return errors.As(err, &providerErr) && providerErr.Code == usageprovider.ErrAuthExpired
}
