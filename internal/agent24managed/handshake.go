// Agent24 fork product patch A24-KB-60 (KB-LITE-01a), A24-KB-62 (KB-LITE-02a): JSON-line handshake on an inherited fd.

package agent24managed

import (
	"encoding/json"
	"fmt"
	"io"
)

// ProcessReady is the first handshake line, sent once the TCP listener is bound and provisioning is done.
// Protocol 2 (A24-KB-62) replaces protocol 1's setup_token with the credential itself.
type ProcessReady struct {
	Type       string     `json:"type"`     // "ready"
	Protocol   int        `json:"protocol"` // 2
	Phase      string     `json:"phase"`    // "process"
	Origin     string     `json:"origin"`   // "http://127.0.0.1:<port>"
	PID        int        `json:"pid"`
	Version    string     `json:"version"`
	Commit     string     `json:"commit"`
	Edition    string     `json:"edition"`
	Credential Credential `json:"credential"`
}

// HandshakeProtocol is the protocol number of both handshake lines.
const HandshakeProtocol = 2

// ModelState is the readiness of one built-in model.
type ModelState struct {
	// Embedding: "ready" | "missing" | "unreachable". Chat: "configured" | "missing" | "unreachable".
	State string `json:"state"`
	Model string `json:"model,omitempty"`
}

// ModelsReady is the second handshake line. WriteModels closes the channel.
type ModelsReady struct {
	Type      string     `json:"type"`     // "models"
	Protocol  int        `json:"protocol"` // 2
	Embedding ModelState `json:"embedding"`
	Chat      ModelState `json:"chat"`
}

// Handshake writes JSON lines to the managed host's inherited channel.
type Handshake struct {
	w io.WriteCloser
}

// OpenHandshake wraps fd: only a pipe or socket, marked close-on-exec so no child process inherits it.
// A refused fd never gets an *os.File, so no finalizer can later close an fd this process still uses.
func OpenHandshake(fd int) (*Handshake, error) {
	f, err := openInheritedPipe(fd, "agent24-handshake")
	if err != nil {
		return nil, err
	}
	return NewHandshake(f), nil
}

// NewHandshake wraps w. Tests use it so the channel is not a real fd.
func NewHandshake(w io.WriteCloser) *Handshake {
	return &Handshake{w: w}
}

// WriteProcess writes one JSON object and a newline. It does not close w
// and does not log the credential.
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
