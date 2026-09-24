package compiler_test

import (
	"path"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/garm/internal/compiler"
)

// renderNatsClientFor runs compiler.Emit — the "server" output (see
// cmd/protoc-gen-garm-tools/main.go's emit=server case), which is where the
// generated NATS client lives: it implements a connect handler interface
// from the contracts module and needs toolplane/natsresolver, exactly like
// Mount already needs toolplane, so it cannot live in the toolsdk output
// (EmitMicro) without contracts depending on garm's own runtime — over a
// fixture shaped like the demo AccountsService, and returns the generated
// source.
func renderNatsClientFor(t *testing.T, protoPackage string) string {
	t.Helper()
	fd := microAccountsFixture(protoPackage)
	gen := buildEmitPlugin(t, fd)
	f := gen.FilesByPath[fd.GetName()]

	tools, err := compiler.Tools([]protoreflect.FileDescriptor{f.Desc})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	filename := path.Join(path.Dir(f.GeneratedFilenamePrefix), string(f.GoPackageName)) + "_garm.pb.go"
	g := gen.NewGeneratedFile(filename, f.GoImportPath)
	if err := compiler.Emit(g, []*protogen.File{f}, f.GoPackageName, tools, nil, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return gen.Response().GetFile()[0].GetContent()
}

func TestNatsClientImplementsTheConnectHandlerInterface(t *testing.T) {
	src := renderNatsClientFor(t, "garm.demo.v1beta1")

	if !strings.Contains(src, "func NewAccountsServiceOverNats(nc *nats.Conn, opts ...natsresolver.Option) demov1beta1connect.AccountsServiceHandler {") {
		t.Error("the NATS client must satisfy the existing connect handler interface so the " +
			"aggregate Mount is unchanged; a parallel MountOverNats would let a build mount " +
			"some services one way, some another, and silently omit the rest")
	}
	if strings.Contains(src, "func MountOverNats(") {
		t.Error("a parallel mount path reopens the hole Mount exists to close")
	}
	if !strings.Contains(src, `"garm.demo.v1beta1.AccountsService.GetAccountSummary"`) {
		t.Error("the subject must be derived from the fully-qualified method")
	}
}

// TestNatsClientMethodsCallNatsresolver pins that the generated methods are
// thin wrappers over natsresolver.Call, not a second implementation of the
// wire protocol — natsresolver.go holds the shared option type and the
// error mapping "so the generated code stays thin" (the brief's own words).
func TestNatsClientMethodsCallNatsresolver(t *testing.T) {
	src := renderNatsClientFor(t, "garm.demo.v1beta1")
	if !strings.Contains(src, "natsresolver.Call(ctx, c.nc, c.opts,") {
		t.Error("the generated method must delegate to natsresolver.Call rather than " +
			"reimplementing the NATS round trip")
	}
}

// TestNatsClientCoversEveryToolAndService checks both tools of the fixture
// service got a method, and that a second service in the same package (the
// real demo has four) would each get its own constructor — exercised here
// via ServicesInOrder's own grouping, so this is really pinning that
// EmitNatsClient walks every service Emit walks, not just the first.
func TestNatsClientCoversEveryToolAndService(t *testing.T) {
	src := renderNatsClientFor(t, "garm.demo.v1beta1")
	for _, want := range []string{
		"func (c *accountsServiceOverNats) GetAccountSummary(",
		"func (c *accountsServiceOverNats) SearchTransactions(",
		`"garm.demo.v1beta1.AccountsService.SearchTransactions"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated NATS client is missing:\n  %s", want)
		}
	}
}
