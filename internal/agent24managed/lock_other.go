//go:build !unix

// Agent24 fork product patch A24-KB-62 (KB-LITE-02a, G2): managed mode is Unix-only.

package agent24managed

import "errors"

func acquireInstanceLock(string) (*InstanceLock, error) {
	return nil, errors.New("managed mode is not supported on this platform")
}
