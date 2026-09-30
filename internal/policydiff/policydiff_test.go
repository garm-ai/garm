package policydiff_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/policydiff"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// tree compiles one proto source into descriptors, so a test can express a
// policy change as the diff between two spellings of the same file.
func tree(t *testing.T, body string) []protoreflect.FileDescriptor {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "d", "v1", "d.proto")
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
	return fds
}

// src builds a one-tool file with the pieces a test wants to vary.
func src(toolPolicy, emailPolicy string) string {
	return `syntax = "proto3";
package d.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/gen/d_v1;x";
option (garm.tool.v1.compartments) = {
  declared: [{ name: "pii" }, { name: "fin" }]
};
message In {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { mask: {} } };
  optional string id = 1;
}
message Out {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_INTERNAL on_deny: { omit: {} } };
  optional string plain = 1;
  optional string email = 2 [(garm.tool.v1.field_policy) = { ` + emailPolicy + ` }];
}
service S {
  rpc Get(In) returns (Out) { option (garm.tool.v1.tool) = { ` + toolPolicy + ` }; }
}
`
}

const baseTool = `name: "get" title: "T" description: "d."
  verb: VERB_READ min_clearance: CLEARANCE_CONFIDENTIAL compartments: ["pii"]`
const baseEmail = `read: CLEARANCE_CONFIDENTIAL compartments: ["pii"] on_deny: { omit: {} }`

func diff(t *testing.T, beforeTool, beforeEmail, afterTool, afterEmail string) []policydiff.Change {
	t.Helper()
	got, err := policydiff.Diff(
		tree(t, src(beforeTool, beforeEmail)),
		tree(t, src(afterTool, afterEmail)))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return got
}

// find asserts exactly one change matching a substring, and returns it.
func find(t *testing.T, cs []policydiff.Change, want string) policydiff.Change {
	t.Helper()
	var hits []policydiff.Change
	for _, c := range cs {
		if strings.Contains(c.What, want) {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one change containing %q, got %d:\n%v", want, len(hits), cs)
	}
	return hits[0]
}

func TestNoChangeIsNoChange(t *testing.T) {
	if got := diff(t, baseTool, baseEmail, baseTool, baseEmail); len(got) != 0 {
		t.Errorf("identical catalogues produced %d change(s): %v", len(got), got)
	}
}

// The direction is the whole product. A diff that listed changes without
// saying which way they moved would be `git diff` with extra steps.
func TestDirectionIsDecidedByTheLattice(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		beforeTool, beforeEmail, afterTool, afterEmail string
		want                                           string
		dir                                            policydiff.Direction
	}{
		{"a tool's clearance lowered", baseTool, baseEmail,
			strings.Replace(baseTool, "CLEARANCE_CONFIDENTIAL", "CLEARANCE_INTERNAL", 1), baseEmail,
			"min_clearance", policydiff.Widening},
		{"a tool's clearance raised", baseTool, baseEmail,
			strings.Replace(baseTool, "CLEARANCE_CONFIDENTIAL", "CLEARANCE_RESTRICTED", 1), baseEmail,
			"min_clearance", policydiff.Narrowing},
		{"a tool's compartment dropped", baseTool, baseEmail,
			strings.Replace(baseTool, ` compartments: ["pii"]`, "", 1), baseEmail,
			`compartment "pii" no longer required`, policydiff.Widening},
		{"a field's clearance lowered", baseTool, baseEmail, baseTool,
			`read: CLEARANCE_INTERNAL compartments: ["pii"] on_deny: { omit: {} }`,
			"read CLEARANCE_CONFIDENTIAL", policydiff.Widening},
		{"a field's compartment dropped", baseTool, baseEmail, baseTool,
			`read: CLEARANCE_CONFIDENTIAL on_deny: { omit: {} }`,
			`compartment "pii" no longer required to read`, policydiff.Widening},
		{"audit_on_read removed", baseTool,
			`read: CLEARANCE_CONFIDENTIAL compartments: ["pii"] audit_on_read: true on_deny: { omit: {} }`,
			baseTool, baseEmail,
			"audit_on_read removed", policydiff.Widening},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := find(t, diff(t, tc.beforeTool, tc.beforeEmail, tc.afterTool, tc.afterEmail), tc.want)
			if c.Direction != tc.dir {
				t.Errorf("%s: direction = %s, want %s\n  %s", tc.want, c.Direction, tc.dir, c.Why)
			}
			if c.Why == "" {
				t.Error("no consequence given; the direction alone does not tell a reviewer what it means")
			}
		})
	}
}

// A verb change is not a widening, and reporting it as one would be worse than
// not reporting it: a widening list people learn to skim is no gate at all.
func TestAVerbChangeIsUnclearRatherThanWidening(t *testing.T) {
	c := find(t, diff(t, baseTool, baseEmail,
		strings.Replace(baseTool, "VERB_READ", "VERB_WRITE", 1), baseEmail), "verb ")
	if c.Direction != policydiff.Unclear {
		t.Errorf("direction = %s, want unclear: a verb change moves a tool between "+
			"caller sets rather than up or down", c.Direction)
	}
}

// Redactions are not ordered. Whether an email domain discloses more than a
// last-four is a judgement about the value, not about the shape.
func TestARedactionChangeIsUnclear(t *testing.T) {
	c := find(t, diff(t, baseTool, baseEmail, baseTool,
		`read: CLEARANCE_CONFIDENTIAL compartments: ["pii"] on_deny: { email_domain: {} }`), "on_deny")
	if c.Direction != policydiff.Unclear {
		t.Errorf("direction = %s, want unclear", c.Direction)
	}
}

// The subtle one, and the reason field policies are RESOLVED rather than read
// as written.
//
// Nothing about `plain` changed. Its message's default did, and `plain`
// inherits it — so every field of that message moved. A diff reading only
// explicit annotations would report nothing at all, and this is the change
// most likely to be made carelessly: it is made in one place and lands
// everywhere.
func TestAChangedMessageDefaultMovesEveryFieldThatInheritsIt(t *testing.T) {
	before := src(baseTool, baseEmail)
	after := strings.Replace(before,
		`message Out {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_INTERNAL on_deny: { omit: {} } };`,
		`message Out {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };`, 1)
	if before == after {
		t.Fatal("the fixture did not change; the replace missed")
	}

	got, err := policydiff.Diff(tree(t, before), tree(t, after))
	if err != nil {
		t.Fatal(err)
	}
	c := find(t, got, "read CLEARANCE_INTERNAL → CLEARANCE_PUBLIC")
	if !strings.Contains(c.Subject, ".plain") {
		t.Errorf("subject = %q, want the inheriting field", c.Subject)
	}
	if c.Direction != policydiff.Widening {
		t.Errorf("direction = %s, want widening", c.Direction)
	}
}

// Tool sets are the dimension that decides REACHABILITY, and this file did not
// read them until v0.21.1. The miss was not theoretical: contracts v0.5.0 gave
// `garm.tasks.v1.decide_task` and `approval_card` `sets: ["triage"]` where they
// had declared none, two named approvers went from not-found to able to decide,
// and `catalogue diff` reported "no policy changes" across it. A reviewer told
// nothing changed about an access grant has been actively misled, which is
// worse than having no tool at all.
//
// These are separate tests rather than a table because the two transitions
// across empty are not the same claim as adding and removing a set on a tool
// that is already scoped, and the reason each one moves the way it does is the
// thing under test.

// Adding a set to a tool that had none is the v0.5.0 case. It is a grant: the
// unscoped caller held it throughout, and every scoped session naming the new
// set gains it.
func TestAToolGainingItsFirstSetIsAGrant(t *testing.T) {
	c := find(t, diff(t, baseTool, baseEmail, baseTool+`
  sets: ["triage"]`, baseEmail), "sets none →")

	if c.Direction != policydiff.Widening {
		t.Errorf("direction = %s, want widening: a tool in no set is refused to "+
			"every scoped session, so declaring one can only add callers", c.Direction)
	}
	// The sentence has to carry the counter-intuitive half, or a reviewer reads
	// "now scoped" as a restriction and approves a grant by mistake.
	for _, want := range []string{"no set", "unscoped"} {
		if !strings.Contains(c.Why, want) {
			t.Errorf("Why does not mention %q, so it does not explain why an empty "+
				"set list is not an absence of policy:\n  %s", want, c.Why)
		}
	}
}

// And the mirror, which is the one most likely to be waved through: dropping a
// tool's last set reads as lifting a restriction and revokes it from every
// scoped session at once.
func TestAToolLosingItsLastSetIsARevocation(t *testing.T) {
	c := find(t, diff(t, baseTool+`
  sets: ["triage"]`, baseEmail, baseTool, baseEmail), "→ none")

	if c.Direction != policydiff.Narrowing {
		t.Errorf("direction = %s, want narrowing: a tool in no set shares none with "+
			"a session that names one", c.Direction)
	}
	if !strings.Contains(c.Why, "opposite") {
		t.Errorf("Why does not warn that this reads backwards:\n  %s", c.Why)
	}
}

func TestASetAddedToAScopedToolWidens(t *testing.T) {
	c := find(t, diff(t,
		baseTool+`
  sets: ["triage"]`, baseEmail,
		baseTool+`
  sets: ["triage", "payments"]`, baseEmail), `set "payments" added`)
	if c.Direction != policydiff.Widening {
		t.Errorf("direction = %s, want widening", c.Direction)
	}
}

func TestASetRemovedFromAScopedToolNarrows(t *testing.T) {
	c := find(t, diff(t,
		baseTool+`
  sets: ["triage", "payments"]`, baseEmail,
		baseTool+`
  sets: ["triage"]`, baseEmail), `set "payments" removed`)
	if c.Direction != policydiff.Narrowing {
		t.Errorf("direction = %s, want narrowing", c.Direction)
	}
}

// A set list that is only reordered is not a change. Sets are a set.
func TestReorderingSetsIsNotAChange(t *testing.T) {
	got := diff(t,
		baseTool+`
  sets: ["triage", "payments"]`, baseEmail,
		baseTool+`
  sets: ["payments", "triage"]`, baseEmail)
	if len(got) != 0 {
		t.Errorf("reordering produced %d change(s): %v", len(got), got)
	}
}

// A new tool's line has to say which sets it lands in, because that is what
// bounds who receives the new surface. "New tool, VERB_READ, INTERNAL" reads
// as available to everyone cleared for it, and a tool in no set is available
// to an unscoped caller and to nobody else.
func TestANewToolSaysWhichSetsItLandsIn(t *testing.T) {
	before := `syntax = "proto3";
package d.v1;
import "garm/tool/v1/tool.proto";
option go_package = "example.com/gen/d_v1;x";
message In { optional string id = 1; }
message Out { optional string plain = 1; }
service S {
  rpc Get(In) returns (Out) { option (garm.tool.v1.tool) = { ` + baseTool + ` }; }
}
`
	after := strings.Replace(before, "service S {", `service S {
  rpc Other(In) returns (Out) { option (garm.tool.v1.tool) = {
    name: "other" title: "O" description: "o." verb: VERB_READ
    min_clearance: CLEARANCE_INTERNAL sets: ["triage"]
  }; }`, 1)

	got, err := policydiff.Diff(tree(t, before), tree(t, after))
	if err != nil {
		t.Fatal(err)
	}
	c := find(t, got, "new tool")
	if !strings.Contains(c.What, `sets ["triage"]`) {
		t.Errorf("a new tool's line does not name its sets: %q", c.What)
	}
}
