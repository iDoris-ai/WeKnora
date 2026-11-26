// Agent24 fork product patch A24-KB-60 (KB-LITE-01a): managed-mode tests.

package agent24managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func TestParseArgsStandaloneUnchanged(t *testing.T) {
	opts, err := ParseArgs([]string{"--port", "1", "--foo"})
	if err != nil {
		t.Fatal(err)
	}
	if opts != (Options{}) {
		t.Fatalf("standalone options = %+v", opts)
	}
}

func TestParseArgsManagedValidation(t *testing.T) {
	fds := []string{"--handshake-fd", "3", "--lifeline-fd", "4"}
	with := func(args ...string) []string { return append(append([]string{}, fds...), args...) }
	if _, err := ParseArgs([]string{"--managed"}); err == nil || !strings.Contains(err.Error(), "state-dir") {
		t.Fatalf("missing --state-dir: %v", err)
	}
	badFD := []string{"--managed", "--state-dir", "x", "--lifeline-fd", "4", "--handshake-fd", "2"}
	if _, err := ParseArgs(badFD); err == nil ||
		!strings.Contains(err.Error(), "handshake-fd") {
		t.Fatalf("handshake fd 2: %v", err)
	}
	if _, err := ParseArgs(with("--managed", "--state-dir", "x", "--port", "70000")); err == nil ||
		!strings.Contains(err.Error(), "port") {
		t.Fatalf("port 70000: %v", err)
	}
	if _, err := ParseArgs(with("--managed", "--state-dir", "x", "--foo")); err == nil ||
		!strings.Contains(err.Error(), "--foo") {
		t.Fatalf("unknown --foo: %v", err)
	}
	opts, err := ParseArgs(with("--managed", "--port=0", "--state-dir=x"))
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Managed || opts.Port != 0 || opts.HandshakeFD != 3 || opts.LifelineFD != 4 ||
		!filepath.IsAbs(opts.StateDir) {
		t.Fatalf("options = %+v", opts)
	}
	if filepath.Base(opts.StateDir) != "x" {
		t.Fatalf("state dir = %s", opts.StateDir)
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr type %T", ln.Addr())
	}
	if !tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listen ip = %v", tcp.IP)
	}
	if tcp.Port <= 0 {
		t.Fatalf("listen port = %d", tcp.Port)
	}

	picked := nonLoopbackIPv4(t)
	if picked == nil {
		t.Log("no non-loopback IPv4 address; asserted loopback bind only")
		return
	}
	target := net.JoinHostPort(picked.String(), strconv.Itoa(tcp.Port))
	conn, err := net.DialTimeout("tcp", target, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("listener accepted a non-loopback dial to %s", target)
	}
}

func nonLoopbackIPv4(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Logf("interface addresses: %v", err)
		return nil
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet == nil || ipnet.IP == nil || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4
		}
	}
	return nil
}

func TestSecretsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	clearSecretEnv(t)
	if err := PrepareSecrets(dir); err != nil {
		t.Fatal(err)
	}
	jwt := os.Getenv("JWT_SECRET")
	aes := os.Getenv("SYSTEM_AES_KEY")
	if len(jwt) < 32 {
		t.Fatalf("jwt length = %d", len(jwt))
	}
	if len(aes) != 32 {
		t.Fatalf("system_aes_key length = %d", len(aes))
	}
	if os.Getenv("DISABLE_REGISTRATION") != "true" {
		t.Fatalf("DISABLE_REGISTRATION = %q", os.Getenv("DISABLE_REGISTRATION"))
	}
	// OIDC first login provisions accounts outside the registration mode, so it must be off too.
	if os.Getenv("OIDC_AUTH_ENABLE") != "false" {
		t.Fatalf("OIDC_AUTH_ENABLE = %q", os.Getenv("OIDC_AUTH_ENABLE"))
	}
	assertSecretModes(t, dir)

	clearSecretEnv(t)
	if err := PrepareSecrets(dir); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("JWT_SECRET") != jwt || os.Getenv("SYSTEM_AES_KEY") != aes {
		t.Fatal("secrets changed across restart")
	}
	if os.Getenv("DISABLE_REGISTRATION") != "true" {
		t.Fatalf("DISABLE_REGISTRATION = %q", os.Getenv("DISABLE_REGISTRATION"))
	}
}

func TestSecretsRefuseConflictingEnv(t *testing.T) {
	dir := t.TempDir()
	clearSecretEnv(t)
	if err := PrepareSecrets(dir); err != nil {
		t.Fatal(err)
	}
	fileJWT, fileAES := readSecretFile(t, dir)
	t.Setenv("JWT_SECRET", "something-else-32-chars-long-xxxxx")
	err := PrepareSecrets(dir)
	if err == nil {
		t.Fatal("expected conflicting JWT_SECRET to fail")
	}
	if strings.Contains(err.Error(), fileJWT) || strings.Contains(err.Error(), fileAES) {
		t.Fatal("error text contains a managed secret")
	}
}

func TestSecretsRefuseLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	clearSecretEnv(t)
	if err := PrepareSecrets(dir); err != nil {
		t.Fatal(err)
	}
	fileJWT, fileAES := readSecretFile(t, dir)
	path := filepath.Join(dir, managedSecretsFilename)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	err := PrepareSecrets(dir)
	if err == nil {
		t.Fatal("expected loose permissions to fail")
	}
	if strings.Contains(err.Error(), fileJWT) || strings.Contains(err.Error(), fileAES) {
		t.Fatal("error text contains a managed secret")
	}
}

func clearSecretEnv(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", "")
	t.Setenv("SYSTEM_AES_KEY", "")
	t.Setenv("DISABLE_REGISTRATION", "")
	t.Setenv("OIDC_AUTH_ENABLE", "true")
	t.Setenv(managedTagKeyEnv, "")
}

func assertSecretModes(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, managedSecretsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %#o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %#o", dirInfo.Mode().Perm())
	}
}

func readSecretFile(t *testing.T, dir string) (string, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, managedSecretsFilename))
	if err != nil {
		t.Fatal(err)
	}
	var parsed managedSecretsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.JWTSecret == "" || parsed.SystemAESKey == "" {
		t.Fatal("secret file is empty")
	}
	return parsed.JWTSecret, parsed.SystemAESKey
}

type handshakeBuffer struct {
	bytes.Buffer
	closed bool
}

func (b *handshakeBuffer) Close() error {
	b.closed = true
	return nil
}

type fakeProbe struct {
	ready ModelsReady
}

func (p fakeProbe) Probe(context.Context) ModelsReady { return p.ready }

func TestHandshakeTokenNotLogged(t *testing.T) {
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	t.Cleanup(func() { logger.SetOutput(os.Stderr) })
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = outW.Close()
		_ = errW.Close()
	}()

	token := "a24k_test-credential-that-must-not-reach-a-log-line"
	buf := &handshakeBuffer{}
	hs := NewHandshake(buf)
	if err := hs.WriteProcess(ProcessReady{
		Type:       "ready",
		Protocol:   HandshakeProtocol,
		Phase:      "process",
		Origin:     "http://127.0.0.1:1",
		PID:        1,
		Version:    "test",
		Commit:     "abc",
		Edition:    "lite",
		Credential: Credential{APIKey: token, KnowledgeBaseID: "kb-1"},
	}); err != nil {
		t.Fatal(err)
	}
	probe := fakeProbe{ready: ModelsReady{
		Type:      "models",
		Protocol:  HandshakeProtocol,
		Embedding: ModelState{State: modelStateMissing},
		Chat:      ModelState{State: modelStateMissing},
	}}
	if err := hs.WriteModels(probe.Probe(context.Background())); err != nil {
		t.Fatal(err)
	}

	os.Stdout, os.Stderr = oldOut, oldErr
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errW.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(outR)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(errR)
	if err != nil {
		t.Fatal(err)
	}

	text := buf.String()
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("handshake = %q", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("handshake lines = %d (%q)", len(lines), text)
	}
	var ready ProcessReady
	if err := json.Unmarshal([]byte(lines[0]), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" || ready.Phase != "process" || ready.Credential.APIKey != token || ready.Protocol != 2 {
		t.Fatalf("ready line = %+v", ready)
	}
	var models ModelsReady
	if err := json.Unmarshal([]byte(lines[1]), &models); err != nil {
		t.Fatal(err)
	}
	if models.Type != "models" {
		t.Fatalf("models type = %q", models.Type)
	}
	if !buf.closed {
		t.Fatal("models line did not close the handshake")
	}
	leaked := logs.String() + string(stdout) + string(stderr)
	if strings.Contains(leaked, token) {
		t.Fatal("setup token appeared outside the handshake channel")
	}
}

type fakeModelService struct {
	interfaces.ModelService
	embedder embedding.Embedder
	openErr  error
}

func (f *fakeModelService) GetEmbeddingModelForTenant(
	context.Context, string, uint64,
) (embedding.Embedder, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.embedder, nil
}

type fakeEmbedder struct {
	embedding.Embedder
	err error
	vec []float32
	got string
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.got = text
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

func TestBuiltinModelProbe(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		db := openProbeDB(t)
		insertModel(t, db, types.Model{
			ID: "user-emb", Name: "user-emb", Type: types.ModelTypeEmbedding,
			Source: types.ModelSourceRemote, IsBuiltin: false, Status: types.ModelStatusActive, TenantID: 1,
		})
		insertModel(t, db, types.Model{
			ID: "gone-emb", Name: "gone-emb", Type: types.ModelTypeEmbedding,
			Source: types.ModelSourceRemote, IsBuiltin: true, Status: types.ModelStatusActive, TenantID: 1,
		})
		if err := db.Delete(&types.Model{}, "id = ?", "gone-emb").Error; err != nil {
			t.Fatal(err)
		}
		got := (&BuiltinModelProbe{DB: db, Models: &fakeModelService{}}).Probe(context.Background())
		if got.Type != "models" || got.Protocol != HandshakeProtocol {
			t.Fatalf("record = %+v", got)
		}
		if got.Embedding.State != modelStateMissing || got.Chat.State != modelStateMissing {
			t.Fatalf("states = %+v %+v", got.Embedding, got.Chat)
		}
	})

	t.Run("embedding ready", func(t *testing.T) {
		db := openProbeDB(t)
		insertModel(t, db, types.Model{
			ID: "emb-1", Name: "embed-a", Type: types.ModelTypeEmbedding,
			Source: types.ModelSourceRemote, IsBuiltin: true, Status: types.ModelStatusActive, TenantID: 7,
		})
		emb := &fakeEmbedder{vec: []float32{0.25}}
		got := (&BuiltinModelProbe{
			DB: db, Models: &fakeModelService{embedder: emb}, Timeout: time.Second,
		}).Probe(context.Background())
		if got.Embedding.State != modelStateReady || got.Embedding.Model != "embed-a" {
			t.Fatalf("embedding = %+v", got.Embedding)
		}
		if emb.got != embedProbeText {
			t.Fatalf("embed text = %q", emb.got)
		}
		if got.Chat.State != modelStateMissing {
			t.Fatalf("chat = %+v", got.Chat)
		}
	})

	t.Run("embedding unreachable", func(t *testing.T) {
		db := openProbeDB(t)
		insertModel(t, db, types.Model{
			ID: "emb-2", Name: "embed-b", Type: types.ModelTypeEmbedding,
			Source: types.ModelSourceRemote, IsBuiltin: true, Status: types.ModelStatusActive, TenantID: 7,
		})
		emb := &fakeEmbedder{err: errors.New("embed failed")}
		got := (&BuiltinModelProbe{
			DB: db, Models: &fakeModelService{embedder: emb},
		}).Probe(context.Background())
		if got.Embedding.State != modelStateUnreachable || got.Embedding.Model != "embed-b" {
			t.Fatalf("embedding = %+v", got.Embedding)
		}
	})

	t.Run("chat ready", func(t *testing.T) {
		db := openProbeDB(t)
		insertModel(t, db, types.Model{
			ID: "chat-1", Name: "chat-a", Type: types.ModelTypeKnowledgeQA,
			Source: types.ModelSourceRemote, IsBuiltin: true, Status: types.ModelStatusActive, TenantID: 7,
		})
		got := (&BuiltinModelProbe{DB: db, Models: &fakeModelService{}}).Probe(context.Background())
		if got.Chat.State != modelStateConfigured || got.Chat.Model != "chat-a" {
			t.Fatalf("chat = %+v", got.Chat)
		}
		if got.Embedding.State != modelStateMissing {
			t.Fatalf("embedding = %+v", got.Embedding)
		}
	})
}

func openProbeDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	dsn := "file:" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&types.Model{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertModel(t *testing.T, db *gorm.DB, m types.Model) {
	t.Helper()
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
}

// TestOpenHandshakeRejectsNonPipe: without a host pipe the fd number can belong to a file this process
// opened (a log file); the setup token must never be written there.
func TestOpenHandshakeRejectsNonPipe(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := OpenHandshake(int(f.Fd())); err == nil {
		t.Fatal("regular file accepted as handshake channel")
	}
	if _, err := f.WriteString("still open"); err != nil {
		t.Fatalf("rejected fd was closed: %v", err)
	}
	// The accepting path is in hardening_unix_test.go (TestOpenHandshakeAcceptsPipe): wrapping an *os.File's
	// fd a second time would leave two finalizers on one fd number.
}
