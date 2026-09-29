package compile_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	garm "github.com/garm-ai/garm"
	cardv1 "github.com/garm-ai/garm/contracts/garm/card/v1"
)

// The card vocabulary gained labels and refs in v0.17.0 (cards-and-tasks
// design §1). Every one of them is an ADDED field or message: nothing was
// renumbered, nothing was removed, and no daemon reads this namespace — so
// the catalogue's schema version, which is garm.tool.v1's alone, must not
// have moved. A test rather than a comment because "additive" is exactly the
// claim a later edit breaks without noticing.
func TestCardVocabularyIsAdditive(t *testing.T) {
	fd, err := protoregistry.GlobalFiles.FindFileByPath("garm/card/v1/card.proto")
	if err != nil {
		t.Fatalf("garm/card/v1/card.proto is not linked: %v", err)
	}

	for _, name := range []protoreflect.Name{"Label", "CallRef", "TaskRef"} {
		if fd.Messages().ByName(name) == nil {
			t.Errorf("garm.card.v1.%s is missing", name)
		}
	}

	// Field numbers are the spec's, verbatim: an Element and a Card label at
	// 10, out of the way of the oneof and of Card's nine existing fields; a
	// Fact at 4 and a Choice at 3, the next free number on each.
	for _, c := range []struct {
		msg   protoreflect.Name
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{"Element", "access", 10},
		{"Card", "access", 10},
		{"Fact", "access", 4},
		{"Fact", "field", 3},
		{"Choice", "access", 3},
	} {
		md := fd.Messages().ByName(c.msg)
		if md == nil {
			t.Fatalf("garm.card.v1.%s is missing", c.msg)
		}
		f := md.Fields().ByName(c.field)
		if f == nil {
			t.Errorf("garm.card.v1.%s.%s is missing", c.msg, c.field)
			continue
		}
		if f.Number() != c.num {
			t.Errorf("garm.card.v1.%s.%s = %d, want %d: the spec fixes these numbers",
				c.msg, c.field, f.Number(), c.num)
		}
	}

	if got := cardv1.Kind_ASK; got != 4 {
		t.Errorf("Kind.ASK = %d, want 4", got)
	}

	// The daemon reads garm.tool.v1 and nothing else. A label is read by
	// garmd at step 8, but it is read off the VALUE of a well-known type,
	// not off an annotation, so the annotation schema is untouched.
	if garm.AnnotationSchemaVersion != 1 {
		t.Errorf("AnnotationSchemaVersion = %d, want 1: adding to garm.card.v1 must not move "+
			"the number a daemon refuses a catalogue by", garm.AnnotationSchemaVersion)
	}
}
