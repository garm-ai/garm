package compiler_test

import (
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compiler"
)

// L34 — a SOURCE_RUNNER field must be one a runner has a rule for.
//
// The only rule this release knows is `idempotency_key` = `<run_id>-<dispatch
// seq>`. A runner field with any other name is a request nobody can send: the
// model does not see it, the caller may not set it, and the runner has nothing
// to put in it.

func sourceSrc(request string) string {
	return `syntax = "proto3";
package s.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/gen/s_v1;x";
message Money {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  optional string currency = 1;
}
message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  optional int64 amount = 1;
  ` + request + `
}
message Out {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
  string idempotency_key = 2 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } source: SOURCE_RUNNER }];
}
service S {
  rpc Do(In) returns (Out) {
    option (garm.tool.v1.tool) = {
      name: "do" title: "T" description: "d."
      verb: VERB_WRITE min_clearance: CLEARANCE_INTERNAL
    };
  }
}
`
}

func l34Diags(ds []compiler.Diag) []compiler.Diag {
	var out []compiler.Diag
	for _, d := range ds {
		if d.Rule == "L34" {
			out = append(out, d)
		}
	}
	return out
}

func TestL34AcceptsARunnerIdempotencyKeyOnTheRequest(t *testing.T) {
	diags := lintSource(t, sourceSrc(
		`string idempotency_key = 2 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER }];`))
	// The response message above carries a SOURCE_RUNNER field too, so this
	// test asserts on the path: the request's key is accepted, the response's
	// is not.
	for _, d := range l34Diags(diags) {
		if d.Path == "s.v1.S.Do(input).idempotency_key" {
			t.Errorf("a request field named idempotency_key with source RUNNER produced L34: %s", d)
		}
	}
	if !hasDiag(diags, "L34", "s.v1.S.Do(output).idempotency_key", "response") {
		t.Errorf("a SOURCE_RUNNER field on the RESPONSE produced no L34 error:\n%s", render(diags))
	}
}

func TestL34RefusesARunnerFieldWithNoRule(t *testing.T) {
	for _, tc := range []struct{ name, field, want string }{
		{"a name the runner has no rule for",
			`string correlation_id = 2 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER }];`,
			`must be named "idempotency_key"`},
		{"the right name but not a string",
			`int64 idempotency_key = 2 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER }];`,
			"must be a singular string"},
		{"the right name but repeated",
			`repeated string idempotency_key = 2 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER }];`,
			"must be a singular string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := lintSource(t, sourceSrc(tc.field))
			if !hasError(diags, "L34", tc.want) {
				t.Errorf("want an L34 error containing %q, got:\n%s", tc.want, render(diags))
			}
		})
	}
}

// A runner fills a field by its path from the request root, and the one rule
// this release knows names a top-level field. Nested, the rule does not
// reach it.
func TestL34RefusesANestedRunnerField(t *testing.T) {
	diags := lintSource(t, sourceSrc(`Money money = 2;`))
	// No L34 for a plain nested message …
	for _, d := range l34Diags(diags) {
		if d.Path == "s.v1.S.Do(input).money.currency" {
			t.Fatalf("a caller field produced L34: %s", d)
		}
	}
	// … but a SOURCE_RUNNER key nested one level down is refused.
	src := sourceSrc(`Nested nested = 2;`) + `
message Nested {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  string idempotency_key = 1 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER }];
}
`
	diags = lintSource(t, src)
	if !hasDiag(diags, "L34", "s.v1.S.Do(input).nested.idempotency_key", "top-level") {
		t.Errorf("a nested SOURCE_RUNNER field produced no L34 error:\n%s", render(diags))
	}
}

// A message default applies to every field, and "every field is the runner's"
// is never what anyone meant.
func TestL34RefusesSourceOnAMessageDefault(t *testing.T) {
	src := sourceSrc(``)
	src = replaceOnce(t, src,
		`message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };`,
		`message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER };`)
	diags := lintSource(t, src)
	if !hasDiag(diags, "L34", "s.v1.S.Do(input)", "default_field_policy") {
		t.Errorf("source on a message default produced no L34 error:\n%s", render(diags))
	}
}

// A caller field — no source, or SOURCE_UNSPECIFIED spelled out — is not
// L34's business, whatever it is called.
func TestL34IgnoresCallerFields(t *testing.T) {
	for _, field := range []string{
		`string idempotency_key = 2;`,
		`string idempotency_key = 2 [(garm.tool.v1.field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_UNSPECIFIED }];`,
		`string correlation_id = 2;`,
	} {
		diags := lintSource(t, sourceSrc(field))
		for _, d := range l34Diags(diags) {
			if d.Path != "s.v1.S.Do(output).idempotency_key" {
				t.Errorf("%s produced L34: %s", field, d)
			}
		}
	}
}

// replaceOnce is strings.Replace with the fixture's own assertion that the
// text it is editing is still there — a silent no-op replacement would test
// the unedited fixture and pass for the wrong reason.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture no longer contains %q", old)
	}
	return strings.Replace(s, old, new, 1)
}
