// Package contractsrepo locates a checkout of github.com/garm-ai/contracts.
//
// It exists only for tests, and only for the two checks in this repository
// whose subject is the contract's SOURCE rather than its compiled Go. The
// module dependency cannot answer them: a linked descriptor carries no proto
// text, and a compiled package carries no generated file this module imports.
//
//   - internal/compiler holds the platform's own protos to this linter, so a
//     contract that could not pass its own rules fails here rather than in a
//     consumer's build.
//   - internal/compiler compares ConnectNames against real connect-go output,
//     which is a checked-in file over there.
//
// Both repositories are normally checked out side by side, so the default
// answer is the sibling directory. CI checks contracts out explicitly and
// points GARM_CONTRACTS_DIR at it.
package contractsrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnvVar overrides where the checkout is looked for. CI sets it; a contributor
// with the two repositories side by side does not need to.
const EnvVar = "GARM_CONTRACTS_DIR"

// Dir returns the root of a garm-ai/contracts checkout.
//
// GARM_CONTRACTS_DIR if it is set — and an error if it is set to something
// that is not one, because a variable pointing at the wrong place must not
// silently fall back to a right one. Otherwise the sibling of this module's
// own root.
func Dir() (string, error) {
	if v := os.Getenv(EnvVar); v != "" {
		if err := verify(v); err != nil {
			return "", fmt.Errorf("%s=%s: %w", EnvVar, v, err)
		}
		return v, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	sibling := filepath.Join(filepath.Dir(root), "contracts")
	if err := verify(sibling); err != nil {
		return "", fmt.Errorf("%s: %w", sibling, err)
	}
	return sibling, nil
}

// Require is Dir for a test: the path, or a skip when there is no checkout to
// read.
//
// A skip rather than a failure, because a contributor who cloned one
// repository should not be told their tests are broken. That would be a hole
// if it were the whole rule, so CI is the other half: there GARM_CONTRACTS_DIR
// is always set, and a skip becomes a failure. A check that can quietly not
// run is not a check.
func Require(t testing.TB) string {
	t.Helper()
	dir, err := Dir()
	if err == nil {
		return dir
	}
	// Set-but-wrong is a failure whatever the environment: somebody named a
	// place, and answering a question about a different place — or not
	// answering it — is worse than saying the name is wrong.
	if os.Getenv(EnvVar) != "" {
		t.Fatalf("%s is set and does not name a checkout: %v", EnvVar, err)
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("no garm-ai/contracts checkout, and CI is set so this check "+
			"must run: %v", err)
	}
	t.Skipf("no garm-ai/contracts checkout (%v); clone it beside this one or "+
		"set %s", err, EnvVar)
	return ""
}

// verify answers whether dir is a garm-ai/contracts checkout, by its go.mod
// rather than by the directory's name: a sibling called "contracts" that is
// something else should say so, not fail later as a missing file.
func verify(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return fmt.Errorf("not a checkout: %w", err)
	}
	const want = "module github.com/garm-ai/contracts"
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == want {
			return nil
		}
	}
	return fmt.Errorf("go.mod does not declare %q", want)
}

// moduleRoot walks up from the working directory to this module's go.mod. Tests
// run in their own package directory, so a relative path to a sibling
// repository would depend on how deep the test is — which is how
// "../../proto" became "../../../contracts/proto" and then wrong again.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(b), "module github.com/garm-ai/garm\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod for github.com/garm-ai/garm above the working directory")
		}
		dir = parent
	}
}
