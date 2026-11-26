//go:build !unix

// Agent24 fork product patch A24-KB-62 (KB-LITE-02a, G1): managed mode is Unix-only.

package agent24managed

import (
	"errors"
	"os"
)

func openInheritedPipePlatform(int, string) (*os.File, error) {
	return nil, errors.New("managed mode is not supported on this platform")
}
