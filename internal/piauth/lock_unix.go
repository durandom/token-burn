//go:build darwin || linux

package piauth

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

func identityFromFile(file *os.File) (fileIdentity, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return fileIdentity{}, fmt.Errorf("stat lock handle: %w", err)
	}
	return fileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func identityFromPath(path string) (fileIdentity, error) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func heartbeatDirectory(file *os.File, now time.Time) error {
	tv := syscall.NsecToTimeval(now.UnixNano())
	if err := syscall.Futimes(int(file.Fd()), []syscall.Timeval{tv, tv}); err != nil {
		return fmt.Errorf("heartbeat lock handle: %w", err)
	}
	return nil
}
