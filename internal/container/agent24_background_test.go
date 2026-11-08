package container

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"go.uber.org/dig"
)

func TestInvokeBackgroundSkipsOutsideStandalone(t *testing.T) {
	for _, tc := range []struct {
		mode string
		runs bool
	}{
		{"", true},
		{handler.Agent24KnowledgeModeStandalone, true},
		{handler.Agent24KnowledgeModeIntegration, false},
		{"bogus", false},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			t.Setenv(handler.Agent24KnowledgeModeEnv, tc.mode)
			ran := false
			invokeBackground(dig.New(), "test", func() { ran = true })
			if ran != tc.runs {
				t.Fatalf("ran = %v, want %v", ran, tc.runs)
			}
			if got := agent24BackgroundJobsDisabled(); got == tc.runs {
				t.Fatalf("agent24BackgroundJobsDisabled() = %v", got)
			}
		})
	}
}

// agent24BackgroundSteps are the container steps that start background work.
// Each must reach dig only through invokeBackground, so integration mode skips it.
var agent24BackgroundSteps = map[string]bool{
	"startTemporaryDocumentCleanup": true,
	"startDataSourceScheduler":      true,
	"startAuditLogRetention":        true,
	"startHousekeepingService":      true,
	"startTenantSkillReaper":        true,
	"startForkSnapshotReaper":       true,
	"registerIMService":             true,
	"RunAsynqServer":                true,
	"RegisterSyncHandlers":          true,
	"recoverPendingWikiTasks":       true,
	// Upstream v0.9.0 (WeKnora#127).
	"startUserMachineGC":     true,
	"startAgentRuntimes":     true,
	"startAgentPromptReaper": true,
	"startShellJobSweeper":   true,
}

// agent24ForegroundInvokes are the steps container.go may pass to
// container.Invoke directly, each checked to start no background work that an
// integration process must not run: cleanup registration, provider and limiter
// registration, chat pipeline plugins, the A24-KB-22 revocation purge and the
// A24-KB-31 UI session purge (both integration-only, on the fork's own tables).
// A new direct Invoke fails the test until it is reviewed and added here or
// moved to invokeBackground.
var agent24ForegroundInvokes = map[string]bool{
	"registerLangfuseCleanup":             true,
	"registerPoolCleanup":                 true,
	"registerMCPCleanup":                  true,
	"registerWebSearchProviders":          true,
	"registerModelConcurrencyLimiter":     true,
	"registerLiteModelConcurrencyLimiter": true,
	"ensureAgent24CredentialRevocations":  true,
	// ensureAgent24UISessions is the A24-KB-31 UI session purge, the same kind of
	// integration-only purge as the A24-KB-22 revocation purge, on the fork's own tables.
	"ensureAgent24UISessions": true,
	// ensureAgent24IngestJobs only creates the A24-KB-41 job table; it starts nothing.
	"ensureAgent24IngestJobs":        true,
	"registerChatLocalImageResolver": true,
	// The BrowserSkill cleanup Promote closure, keyed by its whole text; any
	// other closure, or a change to its body, is unreviewed.
	"func(cleaner interfaces.ResourceCleaner, _ *browserskill.Manager) " +
		`{ cleaner.Promote("BrowserSkill") }`: true,
	// Upstream v0.9.0 wiring steps (WeKnora#127), keyed by their whole text: each
	// only sets dependencies or registers cleanup and starts nothing; the
	// background steps they sit next to go through invokeBackground.
	`func( svc *service.TenantSandboxConfigService, repo ` +
		`interfaces.SandboxAllocationRepository, reg admission.Registry, ) { ` +
		`svc.WithAllocationRepo(repo).WithAdmission(reg) }`: true,
	`func(f *service.SessionForkService, a *sandboxalloc.Service) { f.WithAllocations(a) }`: true,
	`func( p *service.SessionSandboxPinner, skills *service.TenantSkillService, a ` +
		`*sandboxalloc.Service, ) { p.WithPlacer(a) skills.WithPlacer(a) }`: true,
	`func( s interfaces.SessionService, c *sandboxalloc.WorkspaceCleaner, resolver ` +
		`sandbox.TenantSandboxResolver, ) { if handler.Edition == "lite" || resolver == nil { ` +
		`return } if svc, ok := s.(interface { ` +
		`WithWorkspaceCleaner(*sandboxalloc.WorkspaceCleaner) }); ok { svc.WithWorkspaceCleaner(c) ` +
		`} }`: true,
	`func( u interfaces.UserService, c *sandboxalloc.WorkspaceCleaner, resolver ` +
		`sandbox.TenantSandboxResolver, ) { if handler.Edition == "lite" || resolver == nil { ` +
		`return } if svc, ok := u.(interface { WithSandboxReleaser(service.SandboxOwnerReleaser) ` +
		`}); ok { svc.WithSandboxReleaser(c) } }`: true,
	`func( cfgSvc *service.TenantSandboxConfigService, cleaner *sandboxalloc.WorkspaceCleaner, ` +
		`resolver sandbox.TenantSandboxResolver, ) { if handler.Edition == "lite" || resolver == ` +
		`nil { return } cfgSvc.WithUserMachineReleaser(cleaner) }`: true,
	`func(r *service.AgentCollaboration, s interfaces.SessionService, cleaner ` +
		`interfaces.ResourceCleaner) { r.Bind(s) cleaner.RegisterWithName("AgentCollaboration", ` +
		`func() error { ctx, cancel := context.WithTimeout(context.Background(), ` +
		`cleanupStepTimeout) defer cancel() return r.Runtime.Stop(ctx) }) }`: true,
	`func( sessions *session.Handler, mcp *handler.MCPServiceHandler, broker *hitl.Broker, ` +
		`runs *service.SuspendedRunService, rewind *service.SessionRewindService, collaboration ` +
		`*service.AgentCollaboration, automations *service.AutomationService, sessionService ` +
		`interfaces.SessionService, messages *handler.MessageHandler, gate *approval.Gate, streams ` +
		`interfaces.StreamManager, ) { rewind.SetSuspendedRuns(runs) ` +
		`sessions.SetSuspendedRuns(broker, runs) ` +
		`sessions.SetCollaborationStopper(collaboration.Stop) // Automation runs resume under ` +
		`their automation, delegated ones // through the collaboration scheduler; neither as an ` +
		`ordinary chat. sessions.SetDelegatedPromptRouter(func(ctx context.Context, p ` +
		`*types.AgentPrompt) bool { return automations.HandlesPrompt(ctx, p) || ` +
		`collaboration.HandlesPrompt(ctx, p) }) messages.DelegatedPrompts = ` +
		`collaboration.PendingPrompts messages.CollaborationStatuses = ` +
		`collaboration.HistoryStatuses collaboration.BindRecovery(broker, gate, streams) ` +
		`collaboration.ParentEvents = sessions.CollaborationEvents collaboration.ChildEvents = ` +
		`sessions.CollaborationEvents collaboration.ParentSteer = sessions.CollaborationSteer ` +
		`runs.RecoverAnswered = func(ctx context.Context, p *types.AgentPrompt) (bool, error) { if ` +
		`ok, err := automations.RecoverAnswered(ctx, p); ok || err != nil { return ok, err } ` +
		`return collaboration.RecoverAnswered(ctx, p) } automations.Bind(sessionService, broker, ` +
		`runs, streams) automations.Events = sessions.CollaborationEvents ` +
		`automations.StopDelegation = collaboration.StopOwned broker.SetRunHooks(runs.Hooks()) ` +
		`runs.StopDelegation = func(ctx context.Context, p *types.AgentPrompt) error { return ` +
		`collaboration.StopPrompt(ctx, p) } mcp.SetRunResumer(sessions) ` +
		`mcp.SetScheduledPromptRouter(automations.HandlesPrompt) }`: true,
	`func(cleaner interfaces.ResourceCleaner, _ *service.ShellJobService) { ` +
		`cleaner.Promote("LocalShellJobs") }`: true,
}

// invokeArgName names an Invoke argument: a function name, or the whole text
// of a function literal with whitespace collapsed.
func invokeArgName(src []byte, fset *token.FileSet, e ast.Expr) string {
	if lit, ok := e.(*ast.FuncLit); ok {
		text := src[fset.Position(lit.Pos()).Offset:fset.Position(lit.End()).Offset]
		return strings.Join(strings.Fields(string(text)), " ")
	}
	return stepName(e)
}

func stepName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

// TestBackgroundStepsGoThroughInvokeBackground reads container.go: every
// direct container.Invoke is a reviewed foreground step or a chat pipeline
// plugin, every background step is passed to invokeBackground, and
// resetPendingTasks runs only in the else branch of
// agent24BackgroundJobsDisabled.
func TestBackgroundStepsGoThroughInvokeBackground(t *testing.T) {
	src, err := os.ReadFile("container.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "container.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	gated := map[string]bool{}
	resets, guardedResets := 0, 0
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := stepName(call.Fun); fn {
		case "Invoke":
			for _, a := range call.Args {
				name := invokeArgName(src, fset, a)
				plugin := false
				if sel, ok := a.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "chatpipeline" &&
						strings.HasPrefix(sel.Sel.Name, "NewPlugin") {
						plugin = true
					}
				}
				if !plugin && !agent24ForegroundInvokes[name] {
					t.Errorf("%s is passed to Invoke directly: review it, then use invokeBackground "+
						"or add it to agent24ForegroundInvokes", name)
				}
			}
		case "invokeBackground":
			if len(call.Args) == 3 {
				gated[stepName(call.Args[2])] = true
			}
		case "resetPendingTasks":
			resets++
			if inDisabledElse(stack) {
				guardedResets++
			}
		}
		return true
	})
	for name := range agent24BackgroundSteps {
		if !gated[name] {
			t.Errorf("%s is not started through invokeBackground", name)
		}
	}
	if resets == 0 || guardedResets != resets {
		t.Errorf("resetPendingTasks calls = %d, under the integration-mode guard = %d", resets, guardedResets)
	}
}

// inDisabledElse reports whether the innermost node on stack sits in the else
// branch of `if agent24BackgroundJobsDisabled() { ... } else { ... }`.
func inDisabledElse(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		ifs, ok := stack[i].(*ast.IfStmt)
		if !ok {
			continue
		}
		cond, ok := ifs.Cond.(*ast.CallExpr)
		if !ok || stepName(cond.Fun) != "agent24BackgroundJobsDisabled" {
			continue
		}
		return ifs.Else != nil && i+1 < len(stack) && stack[i+1] == ifs.Else
	}
	return false
}
