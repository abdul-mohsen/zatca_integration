package processor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestOnboardSQLDoesNotReferenceLegacyOTPColumn(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	sourcePath := filepath.Join(filepath.Dir(testFile), "onboard.go")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read onboarding source: %v", err)
	}

	file, err := parser.ParseFile(token.NewFileSet(), sourcePath, source, 0)
	if err != nil {
		t.Fatalf("parse onboarding source: %v", err)
	}

	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		query, err := strconv.Unquote(lit.Value)
		if err == nil && strings.Contains(strings.ToLower(query), "zatca_otp") {
			violations = append(violations, query)
		}
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("onboarding SQL references legacy zatca_otp column: %q", violations)
	}
}
