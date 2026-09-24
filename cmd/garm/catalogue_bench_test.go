package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"

	cataloguev1 "github.com/garm-ai/garm/contracts/garm/catalogue/v1"
)

// The catalogue's cost, measured rather than asserted.
//
// Two numbers matter and they are not the same. BUILD cost is paid once by
// whoever publishes. LOAD cost is paid by every daemon at every start — and
// the retained heap is paid for as long as the process runs, which is what
// decides whether a catalogue of a given size belongs in a sidecar.
//
//	go test ./cmd/garm/ -bench=Catalogue -benchtime=1x -run=XXX
//
// The scales are decades because the interesting question is the shape of the
// curve, not a figure at one size. It has been linear everywhere measured.

// benchTree writes a synthetic proto tree with n tools: twenty methods per
// service, each with its own three-field request and response.
//
// Deliberately undocumented protos. Comments live in field_docs and their
// size is a property of the schema rather than of the catalogue, so leaving
// them out measures the floor — a real, well-documented catalogue carries
// more, and the strip that lifts them saves more.
func benchTree(tb testing.TB, n int) string {
	tb.Helper()
	dir := tb.TempDir()

	root := newRoot()
	root.SetArgs([]string{"init", dir})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		tb.Fatal(err)
	}

	const per = 20
	base := filepath.Join(dir, "proto", "bench", "v1")
	if err := os.MkdirAll(base, 0o755); err != nil {
		tb.Fatal(err)
	}
	for s := 0; s*per < n; s++ {
		var b bytes.Buffer
		fmt.Fprintf(&b, "syntax = \"proto3\";\npackage bench.v1.s%d;\n"+
			"import \"garm/tool/v1/tool.proto\";\n"+
			"option go_package = \"example.com/bench/s%d;s%d\";\n\n", s, s, s)
		var methods bytes.Buffer
		for m := 0; m < per && s*per+m < n; m++ {
			fmt.Fprintf(&b, `message In%d {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string account_id = 1;
  optional double amount = 2;
  optional int64 ts = 3;
}
message Out%d {
  option (garm.tool.v1.default_field_policy) = { read: CLEARANCE_PUBLIC on_deny: { omit: {} } };
  optional string id = 1;
  optional double balance = 2;
  optional string status = 3;
}
`, m, m)
			// Tool names are unique across the whole catalogue, so they carry
			// the service index. See L3.
			fmt.Fprintf(&methods, `  rpc M%d(In%d) returns (Out%d) {
    option (garm.tool.v1.tool) = {
      name: "s%d_m%d" title: "Method" description: "A generated benchmark tool."
      verb: VERB_READ min_clearance: CLEARANCE_PUBLIC
    };
  }
`, m, m, m, s, m)
		}
		fmt.Fprintf(&b, "service S%d {\n%s}\n", s, methods.String())
		if err := os.WriteFile(filepath.Join(base, fmt.Sprintf("s%d.proto", s)), b.Bytes(), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	return dir
}

var scales = []int{100, 1000, 10000}

func BenchmarkCatalogueBuild(b *testing.B) {
	for _, n := range scales {
		b.Run(fmt.Sprintf("%dtools", n), func(b *testing.B) {
			dir := benchTree(b, n)
			out := filepath.Join(b.TempDir(), "c.binpb")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root := newRoot()
				root.SetArgs([]string{"catalogue", "build",
					"--proto", filepath.Join(dir, "proto"), "-o", out})
				root.SetOut(&bytes.Buffer{})
				root.SetErr(&bytes.Buffer{})
				if err := root.Execute(); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			st, err := os.Stat(out)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(st.Size())/(1<<20), "MB_artifact")
			b.ReportMetric(float64(st.Size())/float64(n), "B/tool")
		})
	}
}

func BenchmarkCatalogueLoad(b *testing.B) {
	for _, n := range scales {
		b.Run(fmt.Sprintf("%dtools", n), func(b *testing.B) {
			dir := benchTree(b, n)
			out := filepath.Join(b.TempDir(), "c.binpb")
			root := newRoot()
			root.SetArgs([]string{"catalogue", "build",
				"--proto", filepath.Join(dir, "proto"), "-o", out})
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			if err := root.Execute(); err != nil {
				b.Fatal(err)
			}
			body, err := os.ReadFile(out)
			if err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cat := &cataloguev1.Catalogue{}
				if err := proto.Unmarshal(body, cat); err != nil {
					b.Fatal(err)
				}
				if _, err := protodesc.NewFiles(cat.GetFiles()); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()

			// Retained heap, which is the number that decides whether a
			// catalogue of this size belongs beside every pod. Measured with
			// the descriptor set and the raw bytes dropped, because a loader
			// needs neither once the registry exists.
			cat := &cataloguev1.Catalogue{}
			if err := proto.Unmarshal(body, cat); err != nil {
				b.Fatal(err)
			}
			reg, err := protodesc.NewFiles(cat.GetFiles())
			if err != nil {
				b.Fatal(err)
			}
			cat = nil
			var m runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m)
			runtime.KeepAlive(reg)
			b.ReportMetric(float64(m.HeapAlloc)/(1<<20), "MB_retained")
		})
	}
}
