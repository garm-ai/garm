package compiler_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

// L31/L32/L33.
//
// A material field is what a human sees on an approval screen and what the
// grant binds to. A path that resolves to nothing is not a typo you discover
// in production — it is a field silently absent from the digest, so the
// approval covers less than its author believed and nothing says so.

func materialSrc(materialFields, extra string) string {
	return `syntax = "proto3";
package m.v1;
import "buf/validate/validate.proto";
import "garm/tool/v1/tool.proto";
option go_package = "example.com/gen/m_v1;x";
message Money {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  optional string currency = 1 [(buf.validate.field).required = true];
}
message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  optional int64 amount = 1 [(buf.validate.field).required = true];
  optional string note = 2;
  Money money = 3;
  repeated string tags = 4;
  map<string, string> meta = 5;
  ` + extra + `
}
message Out {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
}
service S {
  rpc Do(In) returns (Out) {
    option (garm.tool.v1.tool) = {
      name: "do" title: "T" description: "d."
      verb: VERB_READ min_clearance: CLEARANCE_INTERNAL
      approval: { mode: MODE_GRANT approver_min_clearance: CLEARANCE_RESTRICTED
                  max_grant_age_seconds: 900 ` + materialFields + ` }
    };
  }
}
`
}

func TestAMaterialPathMustResolveToAScalarLeaf(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"a field that does not exist", `material_fields: ["nope"]`, `no field "nope"`},
		{"a message", `material_fields: ["money"]`, "is a message"},
		{"a repeated field", `material_fields: ["tags"]`, "is repeated"},
		{"a map", `material_fields: ["meta"]`, "is a map"},
		{"a path through a scalar", `material_fields: ["amount.x"]`, "continues past it"},
		{"a malformed path", `material_fields: ["a..b"]`, "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := lintSource(t, materialSrc(tc.fields, ""))
			if !hasError(diags, "L32", tc.want) {
				t.Errorf("want an L32 error containing %q, got:\n%s", tc.want, render(diags))
			}
		})
	}
}

func TestAValidMaterialPathIsAccepted(t *testing.T) {
	for _, f := range []string{`material_fields: ["amount"]`,
		`material_fields: ["money.currency"]`,
		`material_fields: ["amount", "money.currency"]`} {
		diags := lintSource(t, materialSrc(f, ""))
		for _, d := range diags {
			if !d.Warn && strings.HasPrefix(d.Rule, "L3") {
				t.Errorf("%s was refused: %s", f, d.String())
			}
		}
	}
}

// L31 — nothing reads material_fields without MODE_GRANT, so setting them
// describes an approval that never happens.
func TestMaterialFieldsWithoutAGrantAreRefused(t *testing.T) {
	src := strings.Replace(materialSrc(`material_fields: ["amount"]`, ""),
		"mode: MODE_GRANT", "mode: MODE_NOTIFY", 1)
	if !hasError(lintSource(t, src), "L31", "without MODE_GRANT") {
		t.Error("material_fields under MODE_NOTIFY was accepted")
	}
}

// L33 — a warning, not an error. Absence is a legitimate thing to approve
// (an unset schedule meaning "now"), and the warning is for the other case:
// a human approving a blank amount that a handler then fills in.
func TestAnOptionalMaterialFieldWarnsRatherThanFails(t *testing.T) {
	diags := lintSource(t, materialSrc(`material_fields: ["note"]`, ""))
	var warned bool
	for _, d := range diags {
		if d.Rule == "L33" {
			if !d.Warn {
				t.Error("L33 is an error; absence is sometimes exactly what is approved")
			}
			warned = true
		}
	}
	if !warned {
		t.Errorf("no L33 for an optional material field:\n%s", render(diags))
	}
	// And a required one does not warn.
	for _, d := range lintSource(t, materialSrc(`material_fields: ["amount"]`, "")) {
		if d.Rule == "L33" {
			t.Errorf("a required field warned: %s", d.String())
		}
	}
}

// These rules need real proto source rather than a hand-built descriptor: the
// cases they exist for are nested messages, repeated fields, maps and
// buf.validate's required constraint, and assembling those by hand would test
// the assembly rather than the rule.

func lintSource(t *testing.T, body string) []compiler.Diag {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "m", "v1", "m.proto")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, fds, err := compile.Tree(context.Background(), dir)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return compiler.Lint(fds)
}

func hasError(ds []compiler.Diag, rule, contains string) bool {
	for _, d := range ds {
		if d.Rule == rule && !d.Warn && strings.Contains(d.Msg, contains) {
			return true
		}
	}
	return false
}

func render(ds []compiler.Diag) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString("  " + d.String() + "\n")
	}
	if b.Len() == 0 {
		return "  (no diagnostics)"
	}
	return b.String()
}
