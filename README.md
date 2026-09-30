# garm

**The command line tool.**

`garm` is what a person types. It scaffolds a proto tree, compiles governed
tool declarations, refuses the bad ones, and builds the catalogue a runtime
will serve. It runs for half a second and exits.

It is not the server. That is [`garmd`](../garmd).

It is not the contract either, any more. The annotations, the vocabularies and
the plan compiler are **[`github.com/garm-ai/contracts`](https://github.com/garm-ai/contracts)**,
a module this one depends on — see [The contract is a dependency](#the-contract-is-a-dependency).

## What you type

```
garm init                scaffold a proto tree, annotations vendored in
garm gen                 run the code generators (a thin wrapper on `buf generate`)
garm lint                check the catalogue.yaml inputs against the L, A, C, O and P rules
garm catalogue init      write the catalogue.yaml that describes this tree
garm catalogue build     compose the catalogue.yaml inputs into the artifact garmd loads
garm catalogue publish   put it, and the prompts it pins, on an object store
garm catalogue diff      what changed between two catalogues, in policy terms
                         (including tool sets, which decide reachability)
garm claims check        assert a claims policy only names vocabulary a catalogue declares
garm plugin              the protoc plugin, so a buf.gen.yaml can name this binary
garm version             what you are running
```

`garm init` writes a proto tree, a `catalogue.yaml` and the four annotation
files into `third_party/proto/`, so an author imports `"garm/tool/v1/tool.proto"`
by its real path without reaching a registry. `garm gen` then shells out to `buf`,
which is a **runtime** dependency of this tool: garm drives buf over your
protos, and buf invokes this binary back as a local plugin.

Upgrading across **v0.19.0** renames the generated card helpers: the default
is now `Default<Service><Card>` and the result builder `<Service><Card>From`,
where a single-tool service or an agent used to get the bare
`DefaultInputCard` and `ResultCardFrom`. A Go identifier is package level
where an RPC name is scoped by its service, so five agent services in one
proto package produced five `DefaultInputCard` and the package did not
compile. The card's RPC name is unchanged, so an **override is unaffected**
and no catalogue digest moved; regenerate, and rename any direct call to a
default. [docs/catalogue.md](docs/catalogue.md) has the rule and why it
differs from the RPC name.

## What is here

| | |
|---|---|
| `cmd/garm/` | The CLI |
| `cmd/protoc-gen-garm-go/` | The protoc plugin, under the conventional name, so `go install` puts it on PATH |
| `internal/catalogue/` | Assembly: the lint gate, the descriptor hashes, the synthesised cards, the marshalled artifact. It decides and returns; the command prints and writes |
| `internal/compile/` | Proto source to descriptors, in process — not by shelling out to buf, so an author needs no second tool and a catalogue's digest does not depend on whichever buf is on someone's PATH |
| `internal/manifest/` | `catalogue.yaml`: what it may say, which version the module graph resolves each entry to, and which input contributed which proto package |
| `internal/compiler/` | The reader, the lint rules, and the emitter |
| `internal/plugin/` | The plugin entry point both binaries share |
| `internal/policydiff/` | What `catalogue diff` compares — and, in `coverage_test.go`, a walk over `ToolPolicy`'s descriptor that refuses a field which is neither compared nor ignored with a reason |
| `conformance/` | Golden proto cases and the diagnostics they must produce |
| `docs/catalogue.md` | What a catalogue is, what it costs, and how it is sized |

The binary **contains** the plugin. One install, not two — and the generator
and the rules it enforces cannot drift apart, because they are the same
artifact.

## The contract is a dependency

The protos are **not here**. `garm.tool.v1`, `garm.agent.v1`, `garm.card.v1`,
`garm.meta.v1` and `garm.tasks.v1`, their generated Go, and the `policy`
package that compiles field annotations into redaction plans all live in
[`garm-ai/contracts`](https://github.com/garm-ai/contracts). This repository
requires that module like any other dependency:

```
require github.com/garm-ai/contracts v0.5.0
```

Why they are not here: ten Go modules compile against the contract and exactly
none of them run this CLI. While the two shared a repository, every consumer of
a message type inherited an S3 client, a CEL evaluator and a CLI framework to
get it, and every release of the tool was a release of the contract. They are
released separately now, and versioned separately.

What that means in practice:

- The annotations `garm init` vendors are read out of the dependency's embed,
  so the bytes in your `third_party/` are the contract's bytes at the version
  this binary was built against.
- The descriptors are **linked, not vendored**: `internal/compile` blank-imports
  the contract's generated packages so their descriptors register in
  `protoregistry.GlobalFiles`, and the compiler resolves your
  `import "garm/tool/v1/tool.proto"` from that registry before it looks on
  disk. So this binary's copy is authoritative and a stale file in somebody's
  `third_party/` cannot quietly change what an annotation means.
- Upgrading across v0.18.0 changes the Go import paths you generate against:
  `github.com/garm-ai/garm/contracts/...` became
  `github.com/garm-ai/contracts/...`, and `github.com/garm-ai/garm/policy`
  became `github.com/garm-ai/contracts/policy`. Re-vendor with
  `garm init --force`, then regenerate.

## The catalogue

`garm catalogue build` composes a catalogue from the inputs **`catalogue.yaml`**
declares, and writes the artifact a daemon loads at boot — so adding a tool is a
catalogue rebuild rather than a release of the governance binary. Two builds of
identical source produce identical bytes, because the digest is the catalogue's
identity.

```yaml
# catalogue.yaml
schema: v1
name: bank
source: garm-ai/examples/bank      # free-form provenance, never parsed

include:
  - path: proto                    # this deployment's own declarations

  - module: github.com/garm-ai/contracts          # platform tools it operates
    packages: [garm.tasks.v1]
  - module: github.com/garm-ai/tools/web          # a tool package it adopts
    packages: [web.v1]

prompts: .

taxonomy:                          # this deployment's own vocabulary
  compartments:
    - name: financial
      description: Money movement and balances.
  tool_sets:
    - name: payments
      description: Initiating and inspecting payments.
```

An input is a directory in this tree or proto packages from a Go module the tree
requires, and the deployment's own tree is one entry among the others rather than
a privileged flag. `garm catalogue build`, with no arguments, is the whole
command: the manifest is found by convention in the working directory.

**The vocabulary is the deployment's.** A tool *requires* compartments by name on
each method; a deployment *declares* which words exist, in `taxonomy:`. Lint then
refuses a name no entry declares — so adopting a tool that needs `internet` is a
decision somebody makes rather than a word that appears. Before v0.22.0 the
vocabulary was unioned out of file-level proto options across every file in the
set, which meant adopting a tool silently extended the vocabulary governing who
may see what, and a deployment could not see its own in one place. Declaring the
key replaces the scrape entirely, and a tree without it keeps the old behaviour.
The words still travel to the daemon on the same two catalogue fields, so this is
not a contract change.

**The manifest names *what*; `go.mod` says *which version*.** Nothing new
fetches anything — a tag is a Go module version, `go mod download` already maps
one to the other, and the protos are read out of the module cache, so this works
offline once the cache is warm and travels through whatever `GOPROXY` an
enterprise already permits. The consequence is the invariant the file exists for:
**a module the tree does not require is refused.** The generated Go already comes
from that requirement, so a second place to pin a version would let a deployment
compile against one tag and declare the descriptors of another, which is a
descriptor mismatch that presents at run time as an agent that cannot mount.
A `version:` key is permitted for readability and must equal what the module
graph resolves to; a disagreement is an error, not a precedence rule.

Three more rules, all so that the artifact means one thing:

- **Two inputs declaring one proto package is refused, naming both.** Never a
  merge and never last-wins — a silent winner is the drift a manifest replaces.
- **A package a module entry names and the module does not declare is refused,
  naming both.**
- **`include` order is presentation.** The inputs are sorted into the order
  `garm.catalogue.v1.Provenance.inputs` specifies before they are stamped, and
  the descriptor set's file order is sorted over every input together, so
  reordering the file does not move the digest.

The artifact then **records what it was composed from**: each input's module path
and resolved version, or the local path and its `source`, with the proto packages
it contributed. That is what makes a catalogue a bill of materials — "which
version of the payment tool's contract is this deployment running" is a question
the artifact answers, where before it was knowable only from a commit.

### Writing the manifest: `garm catalogue init`

A tree that predates the manifest already states everything the file says, spread
across three places, so `garm catalogue init` reads it out of them rather than
asking anyone to retype it: `buf.gen.yaml`'s input directories become `path:`
entries, its `exclude_paths` name the adopted copies, each copy's own
`go_package` says which module it came from, and `go list` resolves that to a
requirement. Then the copies can be deleted, along with the `exclude_paths` and
the drift gate that exist only because of them.

**It does not guess.** Every fact it writes was read from something: which
package a copy declares comes from the compiled descriptor and not from the
directory name. When a copy's module is one the tree does **not** require, §2's
invariant admits no entry — there is no version to pin it to — so there is no
entry, the copy is left exactly as it is, and the report names it and exits
non-zero. A tree in that state compiles its declarations against descriptors it
does not depend on, which is a defect rather than something to encode. It also
refuses to overwrite an existing `catalogue.yaml` without `--force`, because a
hand-edited manifest is the authority and a generator is not.

### Required platform packages, and why lint reads the manifest

A declaration can depend on a capability another **package** provides, and then
the catalogue has to declare that package. Rule **P1**: a tool declaring
`approval { mode: MODE_GRANT }` requires `garm.tasks.v1` in the same catalogue,
and the build refuses one that does not have it.

This is a real outage rather than a tidiness rule. On 2026-09-29 `tasksd` ran all
day serving eight tools while `payments.v1.initiate_payment` declared MODE_GRANT
in a catalogue that did not declare `garm.tasks.v1`. Nothing was wrong with
either half: the daemon mounted the catalogue, the service answered its subject,
and there was no route between them — so the call parked on a task nobody could
open, decide or see, the runner waited out the window and reported that no
approval had arrived, which is indistinguishable from a person declining to act.
Build time is the only place that is cheap to catch.

Whether the package is present is a question about the **assembled** catalogue,
not about one declaration, which is why `garm lint` now takes the same inputs
`catalogue build` does — a manifest found by convention, `-f` to name one
elsewhere, and `--proto` as the deprecated single-directory shorthand. The same
is true of A3's agent allowlist and A9's audience: a linter that saw only the
deployment's own directory would pass a tree the build then refuses. The protoc
plugin, which buf invokes once per directory, cannot answer any of the three and
says it did not check rather than passing silently.

### The flags

| | |
|---|---|
| `-f`, `--manifest` | The manifest to compose from. Default: `catalogue.yaml` in the working directory |
| `-o`, `--out` | Where to write the artifact. Default: `catalogue.binpb` |
| `--source` | Free-form provenance: a repository and commit, a pipeline id. Overrides the manifest's `source:` |
| `--prompts-root` | Where an agent's `prompts.*.path` resolves against. Default: the manifest's `prompts:` |
| `--stamp-time` | Record the build time. Breaks byte-reproducibility, and the help says so |
| `--proto` | **Deprecated.** Build from one directory, as a manifest with a single `path:` entry |

`--proto` still works and produces byte-identical artifacts to the one-entry
manifest that replaces it, because it *is* that manifest: it is turned into one
before anything else happens, so there is one composition path and the deprecated
flag cannot drift away from the supported input. A tree with no `catalogue.yaml`
falls back to `proto/`, so nothing that builds today stops building.

The assembly itself is `internal/catalogue`, the manifest is
`internal/manifest`, and the command is a thin front on both:
`catalogue.Build` is handed a compiled tree and the resolved inputs, and returns
the artifact, its digest and the lint diagnostics. Lint is not a step that front
can skip — `Build` lints before it assembles and refuses on any error, so a
catalogue that does not lint cannot be built by anything that calls it.

About 384 bytes per tool on disk and 9.3 KB retained, linear to at least
10,000 tools — `mise run bench` measures it, and
**[docs/catalogue.md](docs/catalogue.md)** explains what it means for where a
catalogue should be sized.

## Nothing here depends on the server

That is the property this repository exists to hold. An engineer authoring a
tool schema compiles it, lints it and generates from it without running,
installing or cloning [`garmd`](../garmd). Enforcement at authoring time must
not require the thing that enforces at run time.

## What is deliberately not here

- **The contract.** [`garm-ai/contracts`](https://github.com/garm-ai/contracts),
  as above.
- **The governance chain.** Declaring that a tool requires clearance is this
  repository's business; enforcing it is [`garmd`](../garmd)'s.
- **Any taxonomy.** Compartments are an organisation's data-classification
  policy. The contract ships the mechanism — that compartments exist, that
  clearance compares, that a tool declares what it requires — never the
  vocabulary.
- **Commands that need the product.** `manifest`, `agent` and `tool index`
  depend on packages that live in [`garmd`](../garmd) today. They arrive here
  when their dependencies do, and not before — dragging the product across to
  keep a command would defeat the boundary above.

## Working here

```
mise install        the toolchain
mise run test       go test ./... -race
mise run lint       go vet
mise run ci         what CI runs
```

Two tests read a `garm-ai/contracts` checkout, because their subject is the
contract's *source* and a module dependency carries none: the platform's own
protos against this linter, and real connect-go output against `ConnectNames`.
Clone it beside this repository, or set `GARM_CONTRACTS_DIR`. They skip without
one, and fail rather than skip when `CI` is set.

## Status

Shipped: `init`, `gen`, `lint`, `catalogue init|build|diff|publish`,
`claims check`, `plugin`, `version`. `catalogue build` composes from
`catalogue.yaml` since **v0.20.0**; `catalogue init` writes one, `garm lint`
reads one and the required-platform-package rule is enforced since **v0.21.0**.
What the manifest still does not do — the taxonomy, and `publish` resolving a
composed agent's prompts out of its own module — is in KNOWN-GAPS.md.
Thirty-two tool lint rules (the L series), seven agent rules (A1–A5, A9, A10),
three card rules (C1, C8, C9), one ownership rule (O1, a warning) and one
platform-package rule (P1), with twenty conformance cases. What is still missing
is in KNOWN-GAPS.md.
