// Package main is the main package for the WeKnora server
// It contains the main function and the entry point for the server
//
// @title           WeKnora API
// @version         1.0
// @description     WeKnora 知识库管理系统 API 文档
// @termsOfService  http://swagger.io/terms/
//
// @contact.name   WeKnora Github
// @contact.url    https://github.com/Tencent/WeKnora
//
// @BasePath  /api/v1
//
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description 用户登录认证：输入 Bearer {token} 格式的 JWT 令牌

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
// @description API Key 认证：空间 Key 固定访问所属空间；平台 Key 调用空间接口时需同时传 X-Tenant-ID
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24managed"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/container"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func main() {
	// Agent24 A24-KB-01: answer --version before reading config, opening the
	// database or building the container. Package init() of imported packages
	// (e.g. internal/logger with LLM_DEBUG_LOG or LOG_PATH set) has already run.
	if handleVersionFlag(os.Args[1:], os.Stdout) {
		return
	}
	// Agent24 A24-KB-60: --managed is the headless host. Standalone ignores flags.
	opts, err := agent24managed.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "managed mode: %v\n", err)
		os.Exit(2)
	}
	if opts.Managed && handler.Edition != "lite" {
		fmt.Fprintf(os.Stderr, "managed mode requires the lite edition\n")
		os.Exit(2)
	}
	// Agent24 A24-KB-62 (G2): one managed instance per state dir. The OS drops the lock when this process
	// exits, crash included; the lifeline below makes the process exit when its host goes away.
	if opts.Managed {
		lock, lerr := agent24managed.AcquireInstanceLock(opts.StateDir)
		if errors.Is(lerr, agent24managed.ErrInstanceLocked) {
			fmt.Fprintf(os.Stderr, "managed mode: state directory is in use by another instance\n")
			os.Exit(3)
		}
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "managed mode: %v\n", lerr)
			os.Exit(1)
		}
		defer func() { _ = lock.Release() }()
	}
	// Load dotenv configuration before any container provider reads it.
	// Explicit ENV_FILE is honored on every build; automatic .env.lite
	// discovery is scoped to Lite builds (internal/handler.Edition is
	// injected at build time), so a standard binary built in the repo root
	// never picks up Lite defaults. Process variables remain authoritative.
	// A managed host loads no dotenv file at all (A24-KB-62, G3).
	if err := loadDotenv(opts.Managed, handler.Edition == "lite"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	if opts.Managed {
		if err := agent24managed.ApplyManagedEnv(opts.StateDir); err != nil {
			fmt.Fprintf(os.Stderr, "managed env: %v\n", err)
			os.Exit(1)
		}
		if err := agent24managed.PrepareSecrets(opts.StateDir); err != nil {
			fmt.Fprintf(os.Stderr, "managed secrets: %v\n", err)
			os.Exit(1)
		}
	}
	// Dotenv loading happens after the logger package initialized, so
	// re-apply file-based LOG_LEVEL / LOG_PATH (mirrors cmd/desktop).
	logger.ConfigureFromEnv()
	// Set Gin mode
	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}
	// Mute Gin's per-route registration spam (one line per route × ~150
	// routes) — replaced by a single summary printed after router build.
	runtime.SilenceGinRouteSpam()
	// Print the env banner before container build so operators see what
	// config landed even when DB / storage init fails.
	runtime.LogStartupEnv(context.Background())
	runtime.MarkServerStarted()

	var hs *agent24managed.Handshake
	var lifeline *os.File
	if opts.Managed {
		var hsErr error
		hs, hsErr = agent24managed.OpenHandshake(opts.HandshakeFD)
		if hsErr != nil {
			fmt.Fprintf(os.Stderr, "managed handshake: %v\n", hsErr)
			os.Exit(1)
		}
		lifeline, hsErr = agent24managed.OpenLifeline(opts.LifelineFD)
		if hsErr != nil {
			fmt.Fprintf(os.Stderr, "managed lifeline: %v\n", hsErr)
			os.Exit(1)
		}
		// No setup token (A24-KB-62, G4): the credential is provisioned in-process and goes out on the
		// handshake only. SetManagedMode must precede BuildContainer: it keeps background work and the
		// inline ingest executor on in integration mode.
		handler.SetManagedMode(true)
	}

	// Build dependency injection container
	c := container.BuildContainer(runtime.GetContainer())

	// One-shot bootstrap hooks (e.g. promote env-named user to system
	// admin). Best-effort: never aborts startup — see bootstrap.go.
	runStartupBootstrap(c)

	var credential agent24managed.Credential
	if opts.Managed {
		credential = provisionManaged(c)
	}

	// Run application
	err = c.Invoke(func(
		cfg *config.Config,
		router *gin.Engine,
		resourceCleaner interfaces.ResourceCleaner,
		systemSettingSvc interfaces.SystemSettingService,
		modelCatalogSvc *service.ModelCatalogService,
		db *gorm.DB,
		modelSvc interfaces.ModelService,
	) error {
		// Create HTTP server
		server := &http.Server{
			Handler: router,
		}

		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		var listener net.Listener
		var err error
		if opts.Managed {
			listener, err = agent24managed.Listen(opts.Port)
		} else {
			listener, err = listenWithRetry(addr, 10, 300*time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("failed to start server: %v", err)
		}
		if opts.Managed {
			addr = listener.Addr().String()
			// G5: only Host/Origin 127.0.0.1:<port> or localhost:<port> reach the router (DNS rebinding).
			server.Handler = agent24managed.LoopbackGuard(router, listener.Addr().(*net.TCPAddr).Port)
		}

		ctx, done := context.WithCancel(context.Background())
		modelCatalogSvc.Start(ctx)

		// Start the system_settings pubsub subscriber. Runs in its own
		// goroutine and exits when ctx is cancelled at shutdown. Best-
		// effort: an error here only warns (Redis may legitimately be
		// disabled in lite-mode deployments — the service no-ops in
		// that case anyway).
		if err := systemSettingSvc.SubscribeRedis(ctx); err != nil {
			logger.Warnf(ctx, "[system_settings] subscribe failed: %v", err)
		}

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, shutdownSignals...)
		if opts.Managed {
			go agent24managed.WatchLifeline(lifeline, func() {
				logger.Info(context.Background(), "managed host lifeline closed, shutting down")
				select {
				case signals <- syscall.SIGTERM:
				default: // a signal is already queued; shutdown starts either way
				}
			})
		}
		go func() {
			sig := <-signals
			logger.Infof(context.Background(), "Received signal: %v, starting server shutdown...", sig)

			drainBudget, cleanupBudget := runtime.ShutdownBudgets(cfg.Server.ShutdownTimeout)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), drainBudget)
			defer shutdownCancel()

			// Second signal → force close all connections immediately.
			go func() {
				sig := <-signals
				logger.Warnf(context.Background(), "Received second signal: %v, forcing shutdown...", sig)
				server.Close()
			}()

			// Do NOT close the listener manually before Shutdown. Serve would
			// return the raw accept error ("use of closed network connection")
			// instead of ErrServerClosed, and logger.Fatalf → os.Exit(1) would
			// kill the process before ResourceCleaner runs. Shutdown closes
			// listeners first, so the port is released before the drain.
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Errorf(context.Background(), "Server forced to shutdown: %v", err)
				server.Close()
			}

			logger.Info(context.Background(), "Cleaning up resources...")
			// Fresh context sized from the reserved slice of ShutdownTimeout.
			// Drain must not hand cleanup an already-expired context, and the
			// two slices must still add up to one supervisor grace period.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupBudget)
			defer cleanupCancel()
			errs := resourceCleaner.Cleanup(cleanupCtx)
			if len(errs) > 0 {
				logger.Errorf(context.Background(), "Errors occurred during resource cleanup: %v", errs)
			}
			logger.Info(context.Background(), "Server has exited")
			done()
		}()

		runtime.LogGinRouteCount(context.Background())
		logger.Infof(context.Background(), "Server is running at %s", addr)
		if opts.Managed {
			if err := hs.WriteProcess(agent24managed.ProcessReady{
				Type:       "ready",
				Protocol:   agent24managed.HandshakeProtocol,
				Phase:      "process",
				Origin:     "http://" + listener.Addr().String(),
				PID:        os.Getpid(),
				Version:    handler.Version,
				Commit:     handler.CommitID,
				Edition:    handler.Edition,
				Credential: credential,
			}); err != nil {
				return err
			}
			go func() {
				probe := &agent24managed.BuiltinModelProbe{DB: db, Models: modelSvc}
				if merr := hs.WriteModels(probe.Probe(ctx)); merr != nil {
					logger.Warnf(context.Background(), "managed models handshake: %v", merr)
				}
			}()
		}
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server error: %v", err)
		}

		<-ctx.Done()
		return nil
	})
	if err != nil {
		logger.Fatalf(context.Background(), "Failed to run application: %v", err)
	}
}
