package container

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// TestBackgroundStepsGoThroughInvokeBackground reads container.go: no
// background step is passed to container.Invoke directly, every one is passed
// to invokeBackground, and resetPendingTasks runs only in the else branch of
// agent24BackgroundJobsDisabled.
func TestBackgroundStepsGoThroughInvokeBackground(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "container.go", nil, 0)
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
				if agent24BackgroundSteps[stepName(a)] {
					t.Errorf("%s is passed to Invoke directly; use invokeBackground", stepName(a))
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
