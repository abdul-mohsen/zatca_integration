package processor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestOnboardSQLDoesNotWriteLegacyOTP(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	sourcePath := filepath.Join(filepath.Dir(testFile), "onboard.go")
	file, err := parser.ParseFile(token.NewFileSet(), sourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse onboarding source: %v", err)
	}

	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || method.Sel.Name != "Exec" {
			return true
		}

		query, ok := stringLiterals(call.Args[0])
		if ok && strings.Contains(strings.ToLower(query), "zatca_otp") {
			violations = append(violations, query)
		}
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("onboarding SQL writes the legacy zatca_otp column: %q", violations)
	}
}

func stringLiterals(expr ast.Expr) (string, bool) {
	var literals []string
	ast.Inspect(expr, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}

		value, err := strconv.Unquote(literal.Value)
		if err == nil {
			literals = append(literals, value)
		}
		return true
	})
	return strings.Join(literals, " "), len(literals) > 0
}
