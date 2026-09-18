package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRequestPollQueuesRequest(t *testing.T) {
	path := filepath.Join(os.TempDir(), "tb-test-"+strconv.Itoa(os.Getpid())+".sock")
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests, closeControl, err := listenForPollRequests(ctx, path)
	if err != nil {
		t.Fatalf("listenForPollRequests() error = %v", err)
	}
	defer closeControl()

	if err := requestPollPath(ctx, path); err != nil {
		t.Fatalf("requestPollPath() error = %v", err)
	}
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for poll request")
	}
}

func TestRequestPollWithoutDaemonFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := requestPollPath(ctx, filepath.Join(os.TempDir(), "tb-missing-"+strconv.Itoa(os.Getpid())+".sock")); err == nil {
		t.Fatal("requestPollPath() succeeded without daemon")
	}
}
