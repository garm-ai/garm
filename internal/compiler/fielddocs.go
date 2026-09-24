package compiler

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// FieldDocs collects the prose attached to every field in every message,
// keyed by the field's full proto name.
//
// Extracted at build time because SourceCodeInfo does not survive into the
// artifact: it is stripped to halve what a loaded catalogue costs, and the
// comments are the only part of it a schema ever needed.
//
// Leading comments only. A trailing comment on a proto field is almost always
// a note to the next maintainer rather than a description of the value, and
// putting it in front of a model reads as instruction.
func FieldDocs(fds []protoreflect.FileDescriptor) map[string]string {
	docs := map[string]string{}
	for _, fd := range fds {
		locs := fd.SourceLocations()
		var walk func(msgs protoreflect.MessageDescriptors)
		walk = func(msgs protoreflect.MessageDescriptors) {
			for i := 0; i < msgs.Len(); i++ {
				m := msgs.Get(i)
				if m.IsMapEntry() {
					continue
				}
				fields := m.Fields()
				for j := 0; j < fields.Len(); j++ {
					f := fields.Get(j)
					if c := clean(locs.ByDescriptor(f).LeadingComments); c != "" {
						docs[string(f.FullName())] = c
					}
				}
				walk(m.Messages())
			}
		}
		walk(fd.Messages())
	}
	return docs
}

// clean turns a proto comment block into one paragraph of prose.
//
// Comments arrive with a leading space per line and hard wrapping at whatever
// width the author used. Neither is meaningful to a model, and preserving the
// wrapping makes a projected schema look like it has structure it does not.
func clean(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, strings.TrimSpace(line))
	}
	// Blank lines separate paragraphs; collapse each paragraph to one line
	// and keep the breaks between them.
	var paras []string
	var cur []string
	for _, l := range out {
		if l == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, " "))
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, " "))
	}
	return strings.TrimSpace(strings.Join(paras, "\n\n"))
}
