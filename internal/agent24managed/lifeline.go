// Agent24 fork product patch A24-KB-62 (KB-LITE-02a): exit when the host's lifeline pipe closes.

package agent24managed

import (
	"io"
	"os"
	"sync/atomic"
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

// HostWatch follows the lifeline from the moment it is opened. Before Serving is called (container build,
// migrations, listen), the host going away exits the process at once with status 0: nothing is served yet,
// and a stuck start must not keep instance.lock from the host's next launch. After Serving, Gone is closed
// instead and the caller runs its normal graceful shutdown.
type HostWatch struct {
	gone    chan struct{}
	serving atomic.Bool
	exit    func(int)
}

// WatchHost starts watching r in a goroutine. exit is os.Exit in production.
func WatchHost(r io.Reader, exit func(int)) *HostWatch {
	w := &HostWatch{gone: make(chan struct{}), exit: exit}
	go WatchLifeline(r, func() {
		close(w.gone)
		if !w.serving.Load() {
			w.exit(0)
		}
	})
	return w
}

// Gone is closed once the host has gone away. A nil HostWatch returns nil (blocks forever in a select).
func (w *HostWatch) Gone() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.gone
}

// Serving hands the host's departure to the caller: from now on it only closes Gone. Call it after the
// caller selects on Gone; a departure that already happened is still seen there, because Gone stays closed.
func (w *HostWatch) Serving() {
	if w != nil {
		w.serving.Store(true)
	}
}
