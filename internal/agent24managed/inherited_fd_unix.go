//go:build unix

// Agent24 fork product patch A24-KB-62 (KB-LITE-02a, G1): inherited host fds on Unix.

package agent24managed

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openInheritedPipePlatform(fd int, name string) (*os.File, error) {
	if fd < 0 {
		return nil, fmt.Errorf("invalid %s fd %d", name, fd)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("invalid %s fd %d", name, fd)
	}
	// Only a pipe or socket from the host counts. When the host passed nothing, the fd number can
	// already belong to something this process opened (a log file, a device); writing a secret there
	// would leak it. No *os.File exists yet, so a refusal leaves the fd exactly as it was.
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFIFO, unix.S_IFSOCK:
	default:
		return nil, fmt.Errorf("%s fd %d is not a pipe or socket", name, fd)
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}
