package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	garm "github.com/garm-ai/garm"
)

// The annotations are vendored verbatim so that checking for drift later is a
// byte comparison rather than a parse (see embed.go). Four files, not one: a
// tree that vendors the tool annotations and not the agent, card or owner
// ones cannot compile a declaration that uses them at all, and the error it
// gets — "garm/agent/v1/agent.proto: not found" — names a file the author
// never asked for.
func TestInitVendorsEveryAnnotationFileVerbatim(t *testing.T) {
	dir := t.TempDir()
	root := newRoot()
	root.SetArgs([]string{"init", dir})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	for path, want := range map[string][]byte{
		garm.VendoredAnnotationsPath:      garm.AnnotationsProto,
		garm.VendoredAgentAnnotationsPath: garm.AgentAnnotationsProto,
		garm.VendoredCardAnnotationsPath:  garm.CardAnnotationsProto,
		garm.VendoredMetaAnnotationsPath:  garm.MetaAnnotationsProto,
	} {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatalf("%s was not written: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s does not match the embedded bytes; drift checking is a "+
				"byte comparison, so init must write the file verbatim", path)
		}
	}
}

// The vendored paths are part of the contract: an import of
// "garm/agent/v1/agent.proto" has to resolve from the third_party module root,
// and so do the card and owner annotations beside it.
func TestTheVendoredPathsAreUnderThirdPartyProto(t *testing.T) {
	for name, tc := range map[string]struct{ got, want string }{
		"agent": {garm.VendoredAgentAnnotationsPath, "third_party/proto/garm/agent/v1/agent.proto"},
		"card":  {garm.VendoredCardAnnotationsPath, "third_party/proto/garm/card/v1/card.proto"},
		"meta":  {garm.VendoredMetaAnnotationsPath, "third_party/proto/garm/meta/v1/meta.proto"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: vendored path = %q, want %q; the tail is what an import "+
				"resolves against and only the root is a choice", name, tc.got, tc.want)
		}
	}
}
