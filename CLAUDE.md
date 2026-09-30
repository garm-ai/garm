# garm — the CLI

`garm` is the command line tool people type. It scaffolds a proto tree,
compiles governed tool declarations, refuses bad ones, and builds the catalogue
a runtime serves. It is **not** the server; that is `garmd`. It is **not** the
contract; that is `garm-ai/contracts`.

## The four invariants that define this repository

**1. Nothing here may depend on `garmd`.**

An engineer authoring a tool schema must be able to compile, lint and generate
without running, installing or cloning the server. Enforcement at authoring
time must not require the thing that enforces at run time.

Two consequences that look like bugs and are not:

- `internal/compiler` emits import paths for `garmd` packages
  (`toolplanePkg`). Those are `protogen.GoImportPath` **string constants** —
  paths written into generated code, not dependencies of this module. Keep
  them that way.
- The plugin's `emit=server` output references `toolplane` for the same reason:
  it is text this binary writes, for a build that happens elsewhere.

Enforced: CI's `no-daemon-dependency` job greps `go list -deps ./...` for
`garm-ai/garmd` and `/toolplane`.

**2. The contract is a dependency, and this repository must never vendor or
fork it.**

`github.com/garm-ai/contracts` holds the annotations, the four vocabularies,
`garm.tasks.v1`, the generated Go and the `policy` plan compiler. Ten Go
modules compile against it and none of them run this CLI, which is why it is
its own module and its own repository. This one requires it at a released tag
and imports it — nothing more. No `replace`, no `vendor/`, no copy of the tree
back under `contracts/`, `policy/` or `proto/`.

The failure this forbids is not abstract: the two halves lived in one
repository until v0.18.0, and a copy here would be the same code in two places
with two version numbers that have to agree.

Enforced, in three places:

- `mise run contract-is-a-dependency` (in `mise run ci`) fails on a `replace`
  directive, on a `contracts/`, `policy/`, `proto/` or `vendor/` directory
  reappearing, and if `go.mod` stops requiring the module at all.
- `mise run tidy-check` fails if `go.mod`/`go.sum` drifted from tidy, so the
  require cannot be a stale pin nothing resolves.
- CI's `no-daemon-dependency` job also asserts the contract IS reached and does
  not reach back, so the direction stays one-way.

Not enforced, and worth knowing: **which version** of the contract this binary
pins is a plain `require` line, reviewed like any other. Nothing checks that it
is the newest tag.

**3. A catalogue's proto version comes from `go.mod` and from nowhere else.**

`catalogue build` and `garm lint` compose their inputs from `catalogue.yaml`: a
`path:` entry is a directory in the tree, a `module:` entry is proto packages read
out of the Go module cache. The manifest names **what** to include; `go.mod` says **which
version**, because the generated Go already comes from that requirement. So
`internal/manifest` **refuses a module the tree does not require**, and a
`version:` key is readability that has to agree with what the module graph
resolves — a disagreement is an error, never a precedence rule.

The failure this forbids is the one that took the plane offline on 2026-09-29: a
tree compiled against one tag while declaring the descriptors of another serves a
tool whose wire shape does not match its own generated code, and it presents at
run time as a mount that will not complete.

Two consequences to keep straight:

- **`go list -m` is the answer, not a parse of `go.mod`.** Minimal version
  selection, a `replace`, a workspace and an indirect requirement all decide the
  version that is actually built. Reimplementing that here would be a second
  module resolver, wrong in ways nobody notices until the versions disagree. Do
  not replace the exec with `golang.org/x/mod`.
- **A deployment may declare a tool it does not serve**, and `go mod tidy`
  removes a requirement nothing imports. The answer is an explicit blank import
  in the adopting tree, and this repository's job is only to say so: the refusal
  in `internal/manifest.notRequired` names the module, says the tree does not
  require it, and names the blank import. Keep that sentence — without it the
  invariant reads as a bug.
- **`garm catalogue init` reads, it never guesses.** It writes a manifest for a
  tree in the pre-manifest state, and every fact in the file came from something
  the tree already states: `buf.gen.yaml`'s input directories and its
  `exclude_paths`, the compiled descriptor's own package, and the copy's own
  `go_package` resolved to a module by `go list`. A copied package whose module
  the tree does not require gets **no entry and a report** — inventing one would
  be inventing a version, which is the whole thing the invariant forbids.

**4. A rule about the whole catalogue is checked against the whole catalogue.**

`P1` (required platform packages), `A3` (an agent's allowlist) and `A9` (the
audience of what it names) cannot be answered from one directory. So they live in
the group `compiler.Options.PartialSet` turns into warnings: the protoc plugin,
which buf invokes once per directory, **says it did not check** and names the
commands that do. `garm lint` and `garm catalogue build` see the whole assembled
set and enforce — which is why both go through `cmd/garm.compose` and take the
same `inputFlags`. A linter that saw a subset of what the builder composes would
pass a tree the build then refuses, and that is precisely the class of failure
these rules exist to catch early.

One inference IS made under `PartialSet`, and only one: presence is monotone, so
a partial run that CAN see `garm.tasks.v1` concludes P1 is satisfied rather than
warning about a question it has already answered. The negative direction does not
hold and does not conclude.

## What lives where

| | |
|---|---|
| `cmd/garm/` | The CLI: cobra wiring, flags, streams, exit codes and file I/O — and no domain logic. See the rule below |
| `cmd/protoc-gen-garm-go/` | The plugin under its conventional name; one implementation, two binaries |
| `internal/catalogue/` | Catalogue assembly: the lint gate, the per-package descriptor hashes, the synthesised card endpoints, the marshalled artifact. It decides; the command prints |
| `internal/compile/` | Proto source to descriptors, in process. `linked.go` blank-imports the contract's generated packages so `garm/tool/v1/tool.proto` and its siblings resolve from `protoregistry.GlobalFiles` — **that is why this repository holds no protos and needs none.** Do not remove those imports |
| `internal/manifest/` | `catalogue.yaml`: what it may say, what the module graph resolves each entry to, which input contributed which proto package, and — in `adopt.go` — the manifest a pre-manifest tree describes. Shells out to `go list`, deliberately — see the invariants above |
| `internal/compiler/` | Loads protos, lints them, emits code |
| `internal/plugin/` | The plugin entry point, shared by the CLI and the conventionally named binary |
| `internal/policydiff/` | What `catalogue diff` compares. Its `coverage_test.go` walks `ToolPolicy`'s descriptor and refuses a field that is neither compared nor on an ignore list with a reason — see the rule below |
| `internal/contractsrepo/` | Locates a `garm-ai/contracts` checkout, for the two tests whose subject is proto or generated SOURCE |
| `conformance/` | Golden cases. Stays here because it tests `internal/compile` and `internal/compiler` — this repository's compiler |

### The package decides, the command prints

`cmd/garm/` owns flags, output, exit codes and I/O. It does not own judgement.
Anything that decides — what to refuse, what to hash, in what order, what the
artifact contains — belongs in an `internal/` package that takes values and
returns values: no cobra, no `os.WriteFile`, no writing to a stream.

The reason is testability, and it is not theoretical. `catalogue build`'s
assembly lived in a `RunE` until v0.19.1: roughly 125 lines of domain logic
behind 30 of wiring, reachable only by constructing a command and reading its
stderr. So the properties the catalogue actually rests on — that a package's
digest covers the author's declarations and not the synthesised cards, that a
tree with no tools is refused, that a tree which does not lint is never
built — were either untested or tested through a CLI's output. They are
`internal/catalogue`'s tests now.

Two consequences worth keeping straight:

- **`internal/catalogue` reads and writes no files.** `compile.Tree` is called
  by the command and its result handed in; the artifact comes back as bytes the
  command writes. `contracts/policy` has the same shape — it returns a plan and
  leaves applying it to the caller.
- **The lint gate is closed by the API's shape, not by a comment.**
  `catalogue.Build` lints the tree itself and returns `(nil, diags, err)` when
  any diagnostic is an error. The diagnostics come back to be *printed*, never
  for a caller to count: there is no exported way to obtain a `Result` for a
  tree that did not lint, and nothing a caller can pass in to skip it.
  `catalogue.Check` exists for `garm lint`, which reports and gates nothing.

One deliberate exception: `publish`'s prompt verification is still in
`cmd/garm/publish.go`. It already takes values and returns values, so nothing is
untestable there, and its next change is not cosmetic — a composed agent's
prompts live in ITS module, so the single `--prompts-root` becomes a root per
resolved input. `manifest.Input` is now that type and an entry's `prompts:` key
is parsed and carried, so the move is unblocked rather than done. KNOWN-GAPS.md
records it.

### A comparison nobody wrote is a reassurance, not a gap

`internal/policydiff` is the one place in this repository whose failure mode is
**telling somebody that nothing happened**. Everywhere else a missing rule fails
open and the thing it did not refuse eventually surfaces; here a field nobody
compared makes `catalogue diff` print `no policy changes.` across a change that
moved the boundary, and a reviewer acts on that sentence.

It happened once. `sets` was never read, so the contract bump that gave
`garm.tasks.v1.decide_task` and `approval_card` `sets: ["triage"]` — two named
approvers going from `not_found` to able to decide — reported nothing at all. Tool
sets are not a detail of policy: garmd's visibility predicate ends in
`inScope(the session's sets, the tool's sets)`, so membership grants and revokes
**reachability** with no clearance and no compartment moved.

So the rule, and it is not optional when you touch this package:

- **Add a field to `garm.tool.v1.ToolPolicy` or `FieldPolicy` and you add an
  entry to `internal/policydiff/coverage_test.go`.** It walks the descriptor and
  refuses a field that is neither compared nor on an ignore list **with a reason
  beside it** — the reason is the point, because "deliberately not policy" and
  "nobody noticed" are indistinguishable without one. The shape is copied from
  `garm-ai/contracts`' tool-set rule (`garm/tasks/v1/scoping_test.go`) and
  `garm-ai/sink`'s lake columns (`internal/row/contract_test.go`). Do not invent
  a third.
- **"Compared" is proved by moving the field, not by naming it.** Each entry is a
  pair of values differing in one field and the claim that the comparison reports
  it, so deleting an `if` from `policydiff.go` fails the test.
- **Every comparison reports in BOTH directions.** A narrowing nobody intended is
  an outage waiting for the caller who relied on it. Three comparisons were
  one-sided before v0.21.1 — `audit.level` and `audit.retain_days` reported only a
  drop, `audit.fail_closed` only its removal — so every narrowing of those fields
  was invisible. The walk asserts the reverse pair too.
- **Decide the ignore list against what an ENGINE reads, not against what the
  field looks like.** `audit.record_request` looks like a disclosure control and is
  in fact a tool garmd refuses to mount; the inside of an `authorization` block
  looks like a check and reaches no daemon at all. Both are on the list for those
  reasons, and both reasons are in KNOWN-GAPS so that a reader of `diff`'s output
  can bound it.
- **A direction you cannot justify is `Unclear`, and that is a real answer.**
  `audience` is reported that way: it decides who a tool is *offered* to and is
  never read by the predicate that admits an invoke. Putting it in the widening
  bucket would teach people to skim the widening bucket, which is the only outcome
  worse than not reporting at all.

## buf runs outwards now

`buf` is still in `[tools]`, and the direction it runs is the thing to keep
straight. Nothing here is generated by buf: the protos went with the contract,
so there is no `buf.yaml`, no `gen` task and no `gen-check`. What remains is
that `garm gen` shells out to `buf generate` in a **consumer's** tree, and
`internal/plugin` exists so a consumer's `buf.gen.yaml` can name this binary as
a local plugin. So buf is a runtime dependency of the tool, not a build
dependency of the repository.

## Two tests read a sibling checkout

`internal/contractsrepo` finds `../contracts`, or `GARM_CONTRACTS_DIR`. Both
callers ask questions the module dependency cannot answer, because a linked
descriptor carries no proto text and this module imports no generated connect
package:

- `TestTheTasksContractLints` — the platform's own protos against this linter.
- `TestConnectNamingMirror` — real connect-go output against `ConnectNames`.

They **skip** with no checkout and **fail** when `CI` is set. A check that can
quietly not run is not a check, so CI checks the contract out and sets the
variable.

There was a third, briefly. `contracts/KNOWN-GAPS.md` used to name a check
owed by garm: its `policy/testdata/testdatagarm` was hand-written to be
byte-identical to what `cmd/protoc-gen-garm-go` emits, and only this
repository has the plugin. It was written, and then contracts v0.2.0 closed
the gap the better way — the variable is now DERIVED from the file-level
declaration by `policy.DeclaredCompartments`, so it cannot disagree with the
declaration and there is nothing left to transcribe. A property held by
construction beats one held by a comparison, so the check was deleted rather
than kept pointing at a literal that no longer exists. If a fixture over there
ever goes back to hand-writing generated output, this is the repository that
owes the check.

## Do not sed a .pb.go

There is no generated protobuf in this repository any more, and if any appears,
regenerate it rather than editing it. A generated file embeds its
`FileDescriptorProto` as a length-prefixed byte string: rewriting a path inside
one leaves the prefix describing a length that is no longer there, and the
binary panics in `filedesc` at init with a slice bound out of range. This was
learned the expensive way during the contract's extraction.

## Naming, and why it is not what it was

Renamed on extraction from the private monorepo, where different neighbours
forced different names:

- `toolpolicy` → `policy` (now `contracts/policy`). The daemon's app-manifest
  policy stays in `garmd/generation/policy`, so the old collision dissolved
  with the split rather than needing a prefix.
- `internal/toolgen` → `internal/compiler`. Beside `policy`, "toolgen" did not
  say how the two differed. One compiles declarations to code; the other
  compiles declarations to runtime plans.
- Proto package `garm.v1` → `garm.tool.v1`. The annotations were one file
  among ten; the other nine were the daemon's service surface and went with
  it. A tool author now imports the annotations and never sees
  `GenerationService`.

One rule for the code this repository *writes*, rather than the code it is:
**a generated package-level identifier is qualified by its service, always.**
`contracts/cards.methodName` gives a card a bare RPC name on an agent and on a
single-tool service, and that is correct — an RPC name is scoped by the
service it hangs off. A Go identifier is not, so `emit_cards.go` spells
`Default<Service><Card>` and `<Service><Card>From` (see `cardDefaultName`).
Qualifying only where a package happens to collide would make the generated
API depend on what else is in the package; adding a second service would
silently rename the first one's helpers. Do not derive a Go name from
`c.Name` alone.

## Working here

```
mise install        the toolchain
mise run test       go test ./... -race
mise run lint       go vet
mise run ci         what CI runs
```

`mise` is the task runner as well as the toolchain manager. Task names are the
same across every garm-ai repository — that consistency is the point.

## The design record is not in this repository

Specifications, decisions and plans live in the private **`spec`**
repository, checked out beside this one at `../spec/docs/superpowers/`. They
are one interlinked corpus — specs cite each other by section — so they were
not split across repositories, and they are not published.

Read them there when you need the reasoning. The ones that govern this
repository:

- `specs/2026-09-25-public-split-design.md` — why these repositories exist
- `decisions/2026-09-25-garm-is-the-cli.md` — why `garm` is the CLI
- `decisions/2026-09-25-transport-is-a-port.md` — NATS, and what is not swappable
- `specs/2026-09-24-tool-service-shell-design.md` — the contract artifacts
- `specs/2026-09-24-call-stack-design.md` — what the annotations govern

**Do not create `docs/superpowers/` here.** This repository is public; that
record is not.
