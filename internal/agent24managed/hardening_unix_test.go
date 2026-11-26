//go:build unix

package agent24managed

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// rawPipe returns a pipe whose fds are NOT close-on-exec, like fds a host passes to a child.
// The caller owns both fds; an fd handed to OpenHandshake/OpenLifeline is then owned by the returned
// *os.File and must be closed through it only.
func rawPipe(t *testing.T) (r, w int) {
	t.Helper()
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	for _, fd := range p {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			t.Fatal(err)
		}
	}
	return p[0], p[1]
}

const fdProbeChildEnv = "AGENT24_MANAGED_TEST_FD_PROBE"

// TestHelperFDProbe is the child for childSeesFD: it exits 0 when the fd named in the env is open, 1 otherwise.
// (A shell probe is not portable: dash only redirects single-digit fds.)
func TestHelperFDProbe(t *testing.T) {
	v := os.Getenv(fdProbeChildEnv)
	if v == "" {
		t.Skip("helper process only")
	}
	var fd int
	if _, err := fmt.Sscan(v, &fd); err != nil {
		os.Exit(2)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// childSeesFD reports whether a child process started now inherits fd.
func childSeesFD(t *testing.T, fd int) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperFDProbe$")
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", fdProbeChildEnv, fd))
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false
	}
	t.Fatal(err)
	return false
}

// G1: after OpenHandshake / OpenLifeline, child processes no longer inherit the host's fds.
func TestHostFDsNotInheritedByChildren(t *testing.T) {
	for name, open := range map[string]func(int) (func() error, error){
		"handshake": func(fd int) (func() error, error) {
			hs, err := OpenHandshake(fd)
			if err != nil {
				return nil, err
			}
			return hs.w.Close, nil
		},
		"lifeline": func(fd int) (func() error, error) {
			f, err := OpenLifeline(fd)
			if err != nil {
				return nil, err
			}
			return f.Close, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, w := rawPipe(t)
			fd, other := w, r
			if name == "lifeline" {
				fd, other = r, w
			}
			defer func() { _ = syscall.Close(other) }()
			if !childSeesFD(t, fd) {
				_ = syscall.Close(fd)
				t.Fatal("precondition: a child should see a non-cloexec fd")
			}
			closeFD, err := open(fd)
			if err != nil {
				_ = syscall.Close(fd)
				t.Fatal(err)
			}
			defer func() { _ = closeFD() }()
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil {
				t.Fatal(err)
			}
			if flags&unix.FD_CLOEXEC == 0 {
				t.Fatal("fd is not close-on-exec")
			}
			if childSeesFD(t, fd) {
				t.Fatal("child process inherited the fd")
			}
		})
	}
}

// N4: a rejected fd stays open even after garbage collection (no *os.File finalizer closes it).
func TestRejectedFDSurvivesGC(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd())
	if _, err := OpenHandshake(fd); err == nil {
		t.Fatal("regular file accepted as handshake")
	}
	if _, err := OpenLifeline(fd); err == nil {
		t.Fatal("regular file accepted as lifeline")
	}
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := f.WriteString("still open"); err != nil {
		t.Fatalf("rejected fd was closed: %v", err)
	}
}

func TestOpenRejectsClosedFD(t *testing.T) {
	r, w := rawPipe(t)
	_ = syscall.Close(w)
	if err := syscall.Close(r); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLifeline(r); err == nil {
		t.Fatal("closed fd accepted")
	}
}

func TestOpenHandshakeAcceptsPipe(t *testing.T) {
	r, w := rawPipe(t)
	defer func() { _ = syscall.Close(r) }()
	hs, err := OpenHandshake(w)
	if err != nil {
		_ = syscall.Close(w)
		t.Fatalf("pipe rejected: %v", err)
	}
	if err := hs.WriteModels(ModelsReady{Type: "models", Protocol: HandshakeProtocol}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchLifelineFiresOnClose(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	fired := make(chan struct{}, 2)
	go WatchLifeline(r, func() { fired <- struct{}{} })
	if _, err := w.Write([]byte("keepalive")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
		t.Fatal("fired while the host end is open")
	case <-time.After(200 * time.Millisecond):
	}
	_ = w.Close()
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("not fired within 5s of the host closing its end")
	}
	select {
	case <-fired:
		t.Fatal("fired twice")
	case <-time.After(100 * time.Millisecond):
	}
}

// G2: a second instance on the same state dir is refused; the lock is free again after Release.
func TestInstanceLockSecondInstanceRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireInstanceLock(dir); !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("second acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = again.Release()
}

const lifelineChildEnv = "AGENT24_MANAGED_TEST_LIFELINE_CHILD"

// TestHelperLifelineChild is the child process for TestHostCloseFreesInstance: it takes the lock,
// watches stdin as its lifeline and exits when the parent closes it.
func TestHelperLifelineChild(t *testing.T) {
	dir := os.Getenv(lifelineChildEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	if _, err := AcquireInstanceLock(dir); err != nil {
		fmt.Println("lock:", err)
		os.Exit(3)
	}
	fmt.Println("locked")
	done := make(chan struct{})
	go WatchLifeline(os.Stdin, func() { close(done) })
	<-done
	os.Exit(0)
}

// New in Loop 9: when the host goes away (its end of the lifeline closes), the instance exits within
// 5s and the state dir can be locked again, so a crashed host never leaves a Lite that blocks the next start.
func TestHostCloseFreesInstance(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLifelineChild$")
	cmd.Env = append(os.Environ(), lifelineChildEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _ := stdout.Read(buf)
	out.Write(buf[:n])
	if !strings.Contains(out.String(), "locked") {
		t.Fatalf("child did not take the lock: %q", out.String())
	}
	if _, err := AcquireInstanceLock(dir); !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("lock while child runs: %v", err)
	}
	_ = stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("child exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit within 5s of the lifeline closing")
	}
	l, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("lock after child exit: %v", err)
	}
	_ = l.Release()
}

// #181 B2: the host dying during startup (before Serving) exits the process at once; after Serving it only
// closes Gone, and the caller shuts down gracefully.
func TestWatchHostExitsBeforeServingOnly(t *testing.T) {
	t.Run("before serving", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		exited := make(chan int, 1)
		h := WatchHost(r, func(code int) { exited <- code })
		_ = w.Close()
		select {
		case code := <-exited:
			if code != 0 {
				t.Fatalf("exit code %d", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("host gone during startup did not exit")
		}
		select {
		case <-h.Gone():
		default:
			t.Fatal("Gone not closed")
		}
	})
	t.Run("after serving", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		exited := make(chan int, 1)
		h := WatchHost(r, func(code int) { exited <- code })
		h.Serving()
		_ = w.Close()
		select {
		case <-h.Gone():
		case <-time.After(5 * time.Second):
			t.Fatal("Gone not closed after the host left")
		}
		select {
		case <-exited:
			t.Fatal("exited while serving; the caller must shut down gracefully")
		case <-time.After(100 * time.Millisecond):
		}
	})
	var nilWatch *HostWatch
	if nilWatch.Gone() != nil {
		t.Fatal("nil watch must return a nil channel")
	}
	nilWatch.Serving()
}
