package scripts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep script-host launch regressions out of the Go runtime. The optional old
// PS installer and CI's selected shells are compatibility/test entry points,
// not dependencies of the native Windows management path.
func TestNativeRuntimeDoesNotLaunchWindowsScriptHosts(t *testing.T) {
	for _, root := range []string{"../internal", "../cmd"} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				index := 0
				if selector.Sel.Name == "CommandContext" {
					index = 1
				} else if selector.Sel.Name != "Command" {
					return true
				}
				if len(call.Args) <= index {
					return true
				}
				literal, ok := call.Args[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				name, _ := strconv.Unquote(literal.Value)
				switch strings.ToLower(name) {
				case "powershell", "powershell.exe", "pwsh", "pwsh.exe", "wscript.exe", "cscript.exe", "cmd.exe", "schtasks.exe":
					t.Errorf("%s launches script/command host %s", path, name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
