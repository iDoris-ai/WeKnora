// Agent24 fork product patch A24-KB-61 (KB-LITE-02a): exit when the host's lifeline pipe closes.

package agent24managed

import (
	"io"
	"os"
)

// OpenLifeline wraps the inherited read end of a pipe (or a socket) the host keeps open for as long
// as it wants this process to live. Same rules as OpenHandshake: only a pipe or socket, close-on-exec,
// and a rejected fd is left open and untouched.
func OpenLifeline(fd int) (*os.File, error) {
	return openInheritedPipe(fd, "agent24-lifeline")
}

// WatchLifeline blocks until r returns EOF or any error (the host exited, crashed or closed its end),
// then calls onClose exactly once. Bytes the host writes are read and discarded.
func WatchLifeline(r io.Reader, onClose func()) {
	buf := make([]byte, 512)
	for {
		if _, err := r.Read(buf); err != nil {
			break
		}
	}
	if onClose != nil {
		onClose()
	}
}
