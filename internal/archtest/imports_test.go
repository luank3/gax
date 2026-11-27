// Package archtest enforces package layering and platform isolation rules.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const modulePrefix = "github.com/luank3/gax/internal/"

// packageLayer maps each internal package prefix to its architectural layer (ADR-0001).
var packageLayer = map[string]int{
	"core":         0,
	"platform":     1,
	"config":       2,
	"session":      2,
	"catalog":      2,
	"skill":        2,
	"subagent":     2,
	"transcript":   2,
	"transport":    2,
	"wiretrace":    2,
	"bashcompress": 2,
	"web":          2,
	"provider":     3,
	"tools":        3,
	"mcp":          3,
	"agent":        4,
	"tui":          5,
	"oneshot":      5,
	"jsonstream":   5,
	"serve":        5,
}

func layerOf(relPkg string) (int, bool) {
	top := relPkg
	if i := strings.IndexByte(relPkg, '/'); i >= 0 {
		top = relPkg[:i]
	}
	l, ok := packageLayer[top]
	return l, ok
}

func TestLayeringAndPlatformIsolation(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()

	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relDir := filepath.ToSlash(filepath.Dir(rel))
			isPlatform := relDir == "internal/platform" || strings.HasPrefix(relDir, "internal/platform/")

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}

			for _, imp := range file.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				if !isPlatform && (impPath == "syscall" || strings.HasPrefix(impPath, "golang.org/x/sys")) {
					t.Errorf("%s imports %q outside internal/platform", rel, impPath)
				}
				if strings.HasPrefix(relDir, "internal/") && strings.HasPrefix(impPath, modulePrefix) {
					fromPkg := strings.TrimPrefix(relDir, "internal/")
					toPkg := strings.TrimPrefix(impPath, modulePrefix)
					fromTop, _, _ := strings.Cut(fromPkg, "/")
					toTop, _, _ := strings.Cut(toPkg, "/")
					if fromTop == toTop {
						continue
					}
					fromLayer, ok1 := layerOf(fromPkg)
					toLayer, ok2 := layerOf(toPkg)
					if ok1 && ok2 && toLayer >= fromLayer {
						t.Errorf("%s (layer %d) imports %s (layer %d): violates strict downward layering",
							rel, fromLayer, impPath, toLayer)
					}
				}
			}

			if !isPlatform {
				ast.Inspect(file, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					id, ok := sel.X.(*ast.Ident)
					if ok && id.Name == "runtime" && sel.Sel.Name == "GOOS" {
						t.Errorf("%s references runtime.GOOS outside internal/platform", rel)
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}
