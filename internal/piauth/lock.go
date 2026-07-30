package piauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	lockStaleAfter     = 30 * time.Second
	lockHeartbeat      = 2 * time.Second
	lockRetryDelay     = 100 * time.Millisecond
	lockAcquireTimeout = 15 * time.Second
)

var (
	ErrLockUnavailable = errors.New("Pi auth lock unavailable")
	ErrLockCompromised = errors.New("Pi auth lock ownership compromised")
)

type authFileLock struct {
	path      string
	directory *os.File
	identity  fileIdentity
}

func withAuthLock(ctx context.Context, authPath string, fn func(context.Context, *authFileLock) error) error {
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, lockAcquireTimeout)
	defer cancelAcquire()
	lock, err := acquireAuthLock(acquireCtx, authPath+".lock")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLockUnavailable, err)
	}
	lockCtx, cancelLock := context.WithCancel(ctx)
	defer cancelLock()
	heartbeatErr := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(lockHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := lock.heartbeat(); err != nil {
					heartbeatErr <- err
					cancelLock()
					return
				}
			case <-stop:
				return
			}
		}
	}()
	fnErr := fn(lockCtx, lock)
	close(stop)
	<-done
	var compromiseErr error
	select {
	case compromiseErr = <-heartbeatErr:
	default:
		compromiseErr = lock.verifyOwned()
	}
	releaseErr := lock.release()
	if compromiseErr != nil {
		return compromiseErr
	}
	if releaseErr != nil {
		return releaseErr
	}
	return fnErr
}

func acquireAuthLock(ctx context.Context, lockPath string) (*authFileLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("create auth directory: %w", err)
	}
	for {
		err := os.Mkdir(lockPath, 0700)
		if err == nil {
			directory, openErr := os.Open(lockPath)
			if openErr != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("open acquired lock directory: %w", openErr)
			}
			identity, identityErr := identityFromFile(directory)
			if identityErr != nil {
				directory.Close()
				_ = os.Remove(lockPath)
				return nil, identityErr
			}
			lock := &authFileLock{path: lockPath, directory: directory, identity: identity}
			if err := lock.heartbeat(); err != nil {
				_ = lock.release()
				return nil, err
			}
			return lock, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create lock directory: %w", err)
		}
		if removed, staleErr := removeStaleLock(lockPath); staleErr != nil {
			return nil, staleErr
		} else if removed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockRetryDelay):
		}
	}
}

func removeStaleLock(lockPath string) (bool, error) {
	directory, err := os.Open(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("open existing lock directory: %w", err)
	}
	defer directory.Close()
	identity, err := identityFromFile(directory)
	if err != nil {
		return false, err
	}
	info, err := directory.Stat()
	if err != nil {
		return false, fmt.Errorf("stat existing lock directory: %w", err)
	}
	if time.Since(info.ModTime()) <= lockStaleAfter {
		return false, nil
	}
	current, err := identityFromPath(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat existing lock path: %w", err)
	}
	if current != identity {
		return false, nil
	}
	latest, err := directory.Stat()
	if err != nil || !latest.ModTime().Equal(info.ModTime()) {
		return false, err
	}
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove stale lock directory: %w", err)
	}
	return true, nil
}

func (lock *authFileLock) heartbeat() error {
	if err := heartbeatDirectory(lock.directory, time.Now()); err != nil {
		return fmt.Errorf("%w: %v", ErrLockCompromised, err)
	}
	return lock.verifyOwned()
}

func (lock *authFileLock) verifyOwned() error {
	current, err := identityFromPath(lock.path)
	if err != nil {
		return fmt.Errorf("%w: lock path unavailable", ErrLockCompromised)
	}
	if current != lock.identity {
		return fmt.Errorf("%w: lock path was replaced", ErrLockCompromised)
	}
	return nil
}

func (lock *authFileLock) release() error {
	defer lock.directory.Close()
	if err := lock.verifyOwned(); err != nil {
		return err
	}
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release auth lock: %w", err)
	}
	return nil
}
