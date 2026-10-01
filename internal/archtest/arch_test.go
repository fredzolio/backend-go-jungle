// Package archtest holds repository-wide guard tests: architectural rules that the
// compiler cannot enforce.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/fredzolio/backend-go-jungle"

// floatAllowed lists directories (relative to internal/) that may use floats
// because no money flows through them (e.g. Prometheus observations in seconds).
var floatAllowed = []string{
	"platform/metrics/",
}

func sourceFiles(t *testing.T, root string) map[string]*ast.File {
	t.Helper()
	files := map[string]*ast.File{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		files[filepath.ToSlash(path)] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Money must never touch a float: parsing, arithmetic, serialization or SQL.
func TestNoFloatIdentifiersOutsideAllowlist(t *testing.T) {
	banned := []string{"float32", "float64", "ParseFloat", "FormatFloat", "Float64", "Float32"}
	for path, f := range sourceFiles(t, "..") {
		rel := strings.TrimPrefix(path, "../")
		if slices.ContainsFunc(floatAllowed, func(dir string) bool { return strings.HasPrefix(rel, dir) }) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && slices.Contains(banned, id.Name) {
				t.Errorf("%s uses %s", rel, id.Name)
			}
			return true
		})
	}
}

// The domain stays independent of Fx, HTTP, SQS, persistence and outer layers.
func TestDomainImportsOnlyStdlibUUIDAndDomain(t *testing.T) {
	forbiddenStd := []string{"net/http", "database/sql", "log", "log/slog", "os"}
	for path, f := range sourceFiles(t, "../domain") {
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			isStd := !strings.Contains(strings.SplitN(p, "/", 2)[0], ".")
			switch {
			case isStd && !slices.Contains(forbiddenStd, p):
			case p == "github.com/google/uuid":
			case strings.HasPrefix(p, module+"/internal/domain/"):
			default:
				t.Errorf("%s imports %q", path, p)
			}
		}
	}
}
