//go:build unix

// Agent24 fork product patch A24-KB-61 (KB-LITE-02a, G2): flock-based instance lock.

package agent24managed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquireInstanceLock(stateDir string) (*InstanceLock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create managed state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, instanceLockFilename), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrInstanceLocked
		}
		return nil, fmt.Errorf("lock instance: %w", err)
	}
	return &InstanceLock{release: func() error {
		uerr := unix.Flock(int(f.Fd()), unix.LOCK_UN)
		cerr := f.Close()
		if uerr != nil {
			return uerr
		}
		return cerr
	}}, nil
}
