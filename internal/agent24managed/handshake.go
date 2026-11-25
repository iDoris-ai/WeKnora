// Agent24 fork product patch A24-KB-60 (KB-LITE-01a): one-line JSON handshake on an inherited fd.

package agent24managed

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ProcessReady is the first handshake line, sent once the TCP listener is bound.
type ProcessReady struct {
	Type       string `json:"type"`     // "ready"
	Protocol   int    `json:"protocol"` // 1
	Phase      string `json:"phase"`    // "process"
	Origin     string `json:"origin"`   // "http://127.0.0.1:<port>"
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Edition    string `json:"edition"`
	SetupToken string `json:"setup_token"`
}

// ModelState is the readiness of one built-in model.
type ModelState struct {
	State string `json:"state"` // "ready" | "missing" | "unreachable"
	Model string `json:"model,omitempty"`
}

// ModelsReady is the second handshake line. WriteModels closes the channel.
type ModelsReady struct {
	Type      string     `json:"type"`     // "models"
	Protocol  int        `json:"protocol"` // 1
	Embedding ModelState `json:"embedding"`
	Chat      ModelState `json:"chat"`
}

// NewSetupToken returns a 32-byte setup token encoded with base64.RawURLEncoding.
// Callers must not write the token to a log, stdout, or stderr.
func NewSetupToken() (string, error) {
	return randomRawURL(32)
}

// Handshake writes JSON lines to the managed host's inherited channel.
type Handshake struct {
	w io.WriteCloser
}

// OpenHandshake wraps fd. An fd that is not open returns an error.
// The name is fixed so the channel is not confused with a log file.
func OpenHandshake(fd int) (*Handshake, error) {
	if fd < 0 {
		return nil, fmt.Errorf("invalid handshake fd %d", fd)
	}
	f := os.NewFile(uintptr(fd), "agent24-handshake")
	if f == nil {
		return nil, fmt.Errorf("invalid handshake fd %d", fd)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("invalid handshake fd %d", fd)
	}
	// Only a pipe or socket from the host counts. When the host passed nothing,
	// the fd number can already belong to something this process opened (a log
	// file, a device); writing the setup token there would leak it.
	// Not closed: the fd may be one this process still uses.
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return nil, fmt.Errorf("handshake fd %d is not a pipe or socket", fd)
	}
	return NewHandshake(f), nil
}

// NewHandshake wraps w. Tests use it so the channel is not a real fd.
func NewHandshake(w io.WriteCloser) *Handshake {
	return &Handshake{w: w}
}

// WriteProcess writes one JSON object and a newline. It does not close w
// and does not log the setup token.
func (h *Handshake) WriteProcess(r ProcessReady) error {
	return h.writeLine(r)
}

// WriteModels writes one JSON object and a newline, then closes the channel.
func (h *Handshake) WriteModels(r ModelsReady) error {
	err := h.writeLine(r)
	cerr := h.w.Close()
	if err != nil {
		return err
	}
	return cerr
}

func (h *Handshake) writeLine(v any) error {
	if h == nil || h.w == nil {
		return fmt.Errorf("nil handshake")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, err := h.w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
