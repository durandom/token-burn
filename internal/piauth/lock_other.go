//go:build !darwin && !linux

package piauth

import (
	"errors"
	"os"
	"time"
)

type fileIdentity struct{}

var errUnsupportedAuthLockPlatform = errors.New("Pi auth locking is supported on Darwin and Linux")

func identityFromFile(*os.File) (fileIdentity, error) {
	return fileIdentity{}, errUnsupportedAuthLockPlatform
}
func identityFromPath(string) (fileIdentity, error) {
	return fileIdentity{}, errUnsupportedAuthLockPlatform
}
func heartbeatDirectory(*os.File, time.Time) error { return errUnsupportedAuthLockPlatform }
