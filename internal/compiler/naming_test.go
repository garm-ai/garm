package compiler_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compiler"
)

// parseConnectGoOutput reads a real protoc-gen-connect-go output file and
// extracts what TestConnectNamingMirror checks against: the package clause,
// and the name of the <Service>Handler interface (there is exactly one per
// file in this repo's connect.go outputs — one service per file — so "the"
// Handler interface is unambiguous). It is deliberately picky: an
// interface, not any type, and its name must end in "Handler" — a
// connect.go file also declares an UnimplementedXHandler STRUCT (not an
// interface) whose name would otherwise match too.
func parseConnectGoOutput(t *testing.T, path string) (pkgName, handlerIface string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing package clause of %s: %v", path, err)
	}
	pkgName = f.Name.Name

	full, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for _, decl := range full.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, isIface := ts.Type.(*ast.InterfaceType); !isIface {
				continue
			}
			if strings.HasSuffix(ts.Name.Name, "Handler") {
				handlerIface = ts.Name.Name
			}
		}
	}
	if handlerIface == "" {
		t.Fatalf("%s: found no <Service>Handler interface declaration", path)
	}
	return pkgName, handlerIface
}

// TestConnectNamingMirror is the golden test that catches a connect-go
// upgrade changing generated identifiers. It is self-checking: rather than
// comparing ConnectNames against literals hand-copied from today's
// generated files (which a connect-go upgrade would regenerate without
// ever touching this test — exactly the compile-error-in-every-consumer
// failure mode this test exists to catch earlier), it parses the REAL
// checked-in connect.go output with go/parser on every run and compares
// ConnectNames's answer against what that file actually says. A connect-go
// upgrade that changes the package suffix or the handler interface name
// regenerates these files differently, and this test fails on the very
// next `go test` — long before it would otherwise surface as a compile
// error in some other consumer's generated code.
func TestConnectNamingMirror(t *testing.T) {
	for _, c := range []struct {
		goPackageName, service string
		connectGoFile          string
	}{
		{"testdata", "TestService", "../../policy/testdata/testdataconnect/fixture.connect.go"},
	} {
		wantPkg, wantIface := parseConnectGoOutput(t, c.connectGoFile)

		pkg, iface := compiler.ConnectNames(c.goPackageName, c.service)
		if pkg != wantPkg || iface != wantIface {
			t.Fatalf("ConnectNames(%q,%q) = (%q,%q), want (%q,%q) (read from %s)",
				c.goPackageName, c.service, pkg, iface, wantPkg, wantIface, c.connectGoFile)
		}
	}
}
