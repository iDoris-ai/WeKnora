// Agent24 fork product patch A24-KB-62 (KB-LITE-02a, G2): one managed instance per state directory.

package agent24managed

import "errors"

// ErrInstanceLocked means another managed process already holds the state directory.
var ErrInstanceLocked = errors.New("another managed instance holds this state directory")

const instanceLockFilename = "instance.lock"

// InstanceLock is held for the life of the process. The OS drops it when the process exits,
// including on a crash, so a dead instance never blocks the next start.
type InstanceLock struct {
	release func() error
}

// Release drops the lock. A nil lock is a no-op.
func (l *InstanceLock) Release() error {
	if l == nil || l.release == nil {
		return nil
	}
	err := l.release()
	l.release = nil
	return err
}

// AcquireInstanceLock creates stateDir (0700) if needed and takes an exclusive, non-blocking lock on
// <stateDir>/instance.lock. A lock held by another process (or another open of the file in this
// process) returns ErrInstanceLocked.
func AcquireInstanceLock(stateDir string) (*InstanceLock, error) {
	return acquireInstanceLock(stateDir)
}
