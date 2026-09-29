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
garm lint                check your tool declarations against the L, A, C and O rules
garm catalogue build     compile the proto tree into the artifact garmd loads
garm catalogue publish   put it, and the prompts it pins, on an object store
garm catalogue diff      what changed between two catalogues, in policy terms
garm claims check        assert a claims policy only names vocabulary a catalogue declares
garm plugin              the protoc plugin, so a buf.gen.yaml can name this binary
garm version             what you are running
```

`garm init` writes a proto tree and vendors the four annotation files into
`third_party/proto/`, so an author imports `"garm/tool/v1/tool.proto"` by its
real path without reaching a registry. `garm gen` then shells out to `buf`,
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
| `internal/compile/` | Proto source to descriptors, in process — not by shelling out to buf, so an author needs no second tool and a catalogue's digest does not depend on whichever buf is on someone's PATH |
| `internal/compiler/` | The reader, the lint rules, and the emitter |
| `internal/plugin/` | The plugin entry point both binaries share |
| `internal/policydiff/` | What `catalogue diff` compares |
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
require github.com/garm-ai/contracts v0.2.0
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

`garm catalogue build` compiles a proto tree into the artifact a daemon loads
at boot, so adding a tool is a catalogue rebuild rather than a release of the
governance binary. Two builds of identical source produce identical bytes,
because the digest is the catalogue's identity.

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

Shipped: `init`, `gen`, `lint`, `catalogue build|diff|publish`, `claims check`,
`plugin`, `version`. Thirty-two tool lint rules (the L series), seven agent rules
(A1–A5, A9, A10), three card rules (C1, C8, C9) and one ownership rule (O1, a
warning), with twenty conformance cases. What is still missing is in
KNOWN-GAPS.md.
