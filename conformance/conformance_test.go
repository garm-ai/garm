// Package conformance pins what garm's tool rules accept and refuse, as
// readable files rather than as assertions buried in a unit test.
//
// The audience is a person writing a tool schema, not a person maintaining
// the linter. Each case is a .proto you can read and an expected.txt that
// says exactly what garm says about it — so "why was this rejected" and "what
// does an acceptable declaration look like" are answered by the same
// directory.
//
// Regenerate the expectations after a deliberate rule change:
//
//	go test ./conformance/ -update
//
// and read the diff. A rule change that alters a message is a change to the
// contract with everyone writing schemas, and it should be reviewed as one.
package conformance_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garm-ai/garm/internal/compile"
	"github.com/garm-ai/garm/internal/compiler"
)

var update = flag.Bool("update", false, "rewrite expected.txt from current behaviour")

func TestConformance(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("cases", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no cases found; this test passing with nothing to check would be worse than failing")
	}

	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			// Each case compiles alone, with the annotations resolved from
			// the linked registry rather than from a vendored copy — so a
			// case is one file and says only what it is about.
			_, fds, err := compile.Tree(t.Context(), dir)
			if err != nil {
				t.Fatalf("compiling %s: %v", dir, err)
			}

			var got []string
			for _, d := range compiler.Lint(fds) {
				got = append(got, d.String())
			}
			actual := strings.Join(got, "\n")
			if actual != "" {
				actual += "\n"
			}

			path := filepath.Join(dir, "expected.txt")
			if *update {
				if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v (run with -update to create it)", path, err)
			}
			if actual != string(want) {
				t.Errorf("diagnostics changed.\n--- want ---\n%s\n--- got ---\n%s\n"+
					"If this change is deliberate, run `go test ./conformance/ -update` "+
					"and review the diff: these messages are the contract with everyone "+
					"writing schemas.", want, actual)
			}
		})
	}
}
