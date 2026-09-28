package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	garm "github.com/garm-ai/garm"
)

// The annotations are vendored verbatim so that checking for drift later is a
// byte comparison rather than a parse (see embed.go). Two files now, not one:
// a tree that vendors the tool annotations and not the agent ones cannot
// compile an agent declaration at all, and the error it gets —
// "garm/agent/v1/agent.proto: not found" — names a file the author never
// asked for.
func TestInitVendorsBothAnnotationFilesVerbatim(t *testing.T) {
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
// "garm/agent/v1/agent.proto" has to resolve from the third_party module root.
func TestTheVendoredAgentPathIsUnderThirdPartyProto(t *testing.T) {
	const want = "third_party/proto/garm/agent/v1/agent.proto"
	if garm.VendoredAgentAnnotationsPath != want {
		t.Errorf("VendoredAgentAnnotationsPath = %q, want %q; the tail is what an "+
			"import resolves against and only the root is a choice",
			garm.VendoredAgentAnnotationsPath, want)
	}
}
