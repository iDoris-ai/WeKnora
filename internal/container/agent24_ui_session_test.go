// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package container

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func TestEnsureAgent24UISessionsStandalone(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeStandalone)
	db := openAgent24RevocationDB(t)
	cleaner := &agent24RevocationCleaner{}
	require.NoError(t, ensureAgent24UISessions(db, cleaner))
	require.False(t, db.Migrator().HasTable(&types.Agent24UITicket{}))
	require.False(t, db.Migrator().HasTable(&types.Agent24UIFamily{}))
	require.Nil(t, cleaner.fn)
}

func TestEnsureAgent24UISessionsIntegration(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeIntegration)
	db := openAgent24RevocationDB(t)
	cleaner := &agent24RevocationCleaner{}
	t.Cleanup(func() { _ = cleaner.Cleanup(context.Background()) })
	require.NoError(t, ensureAgent24UISessions(db, cleaner))
	require.True(t, db.Migrator().HasTable(&types.Agent24UITicket{}))
	require.True(t, db.Migrator().HasTable(&types.Agent24UIFamily{}))
	require.Equal(t, "Agent24UISessionPurge", cleaner.name)
	require.NotNil(t, cleaner.fn)
	var _ interfaces.ResourceCleaner = cleaner
}

// TestBuildContainerInvokesAgent24UISessions pins the startup wiring. dig's DryRun does not run Invoke,
// so the call is read from the source: BuildContainer must call must(container.Invoke(ensureAgent24UISessions))
// at its top level, outside any branch such as AUTO_MIGRATE and with no return before it, so the tables exist
// whenever integration mode starts.
func TestBuildContainerInvokesAgent24UISessions(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "container.go", nil, 0)
	require.NoError(t, err)
	var build *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "BuildContainer" {
			build = fn
		}
	}
	require.NotNil(t, build, "BuildContainer not found")
	found := 0
	for _, stmt := range build.Body.List {
		if agent24IsMustInvoke(stmt, "ensureAgent24UISessions") {
			found++
			continue
		}
		if found == 0 {
			require.False(t, agent24ReturnsEarly(stmt),
				"BuildContainer must not return before it invokes ensureAgent24UISessions")
		}
	}
	require.Equal(t, 1, found, "BuildContainer must invoke ensureAgent24UISessions once, unconditionally")
	require.Equal(t, 1, agent24CountInvokes(build.Body, "ensureAgent24UISessions"),
		"no other Invoke of ensureAgent24UISessions, in a block, defer, go or function literal")
	require.False(t, agent24HasGoto(build.Body), "a goto in BuildContainer could jump over the call")
}
