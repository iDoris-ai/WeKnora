// Agent24 fork product patch A24-KB-62 (KB-LITE-02a, G1): inherited host fds.

package agent24managed

import "os"

// openInheritedPipe checks fd with fstat before wrapping it in an *os.File, so a rejected fd never
// gets an *os.File (whose finalizer would close an fd this process may still use). Only a pipe or
// a socket is accepted. On success the fd is marked close-on-exec, so child processes (docreader,
// parsers, anything exec'd later) do not inherit it.
func openInheritedPipe(fd int, name string) (*os.File, error) {
	return openInheritedPipePlatform(fd, name)
}
