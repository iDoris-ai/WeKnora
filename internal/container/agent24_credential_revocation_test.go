package container

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24RevocationCleaner is the smallest ResourceCleaner. The container tests have no existing double.
type agent24RevocationCleaner struct {
	name string
	fn   types.CleanupFunc
}

func (c *agent24RevocationCleaner) Register(fn types.CleanupFunc) { c.fn = fn }

func (c *agent24RevocationCleaner) RegisterWithName(name string, fn types.CleanupFunc) {
	c.name = name
	c.fn = fn
}

func (c *agent24RevocationCleaner) Promote(string) {}

func (c *agent24RevocationCleaner) Cleanup(context.Context) []error {
	if c.fn == nil {
		return nil
	}
	if err := c.fn(); err != nil {
		return []error{err}
	}
	return nil
}

func openAgent24RevocationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func TestEnsureAgent24CredentialRevocationsStandalone(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeStandalone)
	db := openAgent24RevocationDB(t)
	cleaner := &agent24RevocationCleaner{}
	require.NoError(t, ensureAgent24CredentialRevocations(db, cleaner))
	require.False(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
	require.Nil(t, cleaner.fn)
}

func TestEnsureAgent24CredentialRevocationsIntegration(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeIntegration)
	db := openAgent24RevocationDB(t)
	cleaner := &agent24RevocationCleaner{}
	t.Cleanup(func() { _ = cleaner.Cleanup(context.Background()) })
	require.NoError(t, ensureAgent24CredentialRevocations(db, cleaner))
	require.True(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
	require.Equal(t, "Agent24CredentialRevocationPurge", cleaner.name)
	require.NotNil(t, cleaner.fn)
	var _ interfaces.ResourceCleaner = cleaner
}

// TestBuildContainerInvokesAgent24CredentialRevocations pins the startup wiring (N14 on #120). dig's DryRun
// does not run Invoke, so the call is read from the source: BuildContainer must call
// must(container.Invoke(ensureAgent24CredentialRevocations)) at its top level, outside any branch such as
// AUTO_MIGRATE and with no return before it, so the table exists whenever integration mode starts.
func TestBuildContainerInvokesAgent24CredentialRevocations(t *testing.T) {
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
		if agent24IsMustInvoke(stmt, "ensureAgent24CredentialRevocations") {
			found++
			continue
		}
		if found == 0 {
			require.False(t, agent24ReturnsEarly(stmt),
				"BuildContainer must not return before it invokes ensureAgent24CredentialRevocations")
		}
	}
	require.Equal(t, 1, found, "BuildContainer must invoke ensureAgent24CredentialRevocations once, unconditionally")
}

// agent24ReturnsEarly reports whether stmt holds a return of BuildContainer itself, that is, one outside a
// function literal.
func agent24ReturnsEarly(stmt ast.Stmt) bool {
	returns := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			returns = true
		}
		return !returns
	})
	return returns
}

// agent24IsMustInvoke reports whether stmt is must(container.Invoke(name)), on BuildContainer's own container.
func agent24IsMustInvoke(stmt ast.Stmt, name string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	must, ok := expr.X.(*ast.CallExpr)
	if !ok || len(must.Args) != 1 {
		return false
	}
	if id, ok := must.Fun.(*ast.Ident); !ok || id.Name != "must" {
		return false
	}
	invoke, ok := must.Args[0].(*ast.CallExpr)
	if !ok || len(invoke.Args) != 1 {
		return false
	}
	sel, ok := invoke.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Invoke" {
		return false
	}
	if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "container" {
		return false
	}
	arg, ok := invoke.Args[0].(*ast.Ident)
	return ok && arg.Name == name
}
