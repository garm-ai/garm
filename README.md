# garm

**The command line tool, and the contract language it speaks.**

`garm` is what a person types. It scaffolds a proto tree, compiles governed
tool declarations, refuses the bad ones, and builds the catalogue a runtime
will serve. It runs for half a second and exits.

It is not the server. That is [`garmd`](../garmd).

## What is here

| | |
|---|---|
| `proto/garm/v1/` | The annotations — verb, clearance, compartments, tool sets, effects, guidance, approval, audit |
| `cmd/garm/` | The CLI |
| `internal/toolgen/` | The reader, the emitter, and the lint rules |
| `conformance/` | Golden proto sets and the errors they must produce |

## What you type

```
garm init                scaffold a proto tree, annotations vendored in
garm gen                 run the code generators
garm lint                check your tool declarations
garm new toolservice     scaffold a service
garm catalogue build     build the artifact garmd loads
garm catalogue check     will this load on garmd v2.1?
```

The binary also **contains** `protoc-gen-garm-go` and
`protoc-gen-garm-python`, invoked by buf as local plugins. One install, not
three — and the annotations and the generator that reads them cannot drift
apart, because they are the same artifact.

## Why the annotations live with the CLI

They have the same consumers, the same release cadence, and no build edge
between them. A new annotation and the code generation that reads it are one
change; split across two repositories they become a release dance with a
window in which the annotation exists and nothing can compile it.

The annotations are additionally published as a buf module, for anyone with a
pipeline that prefers a registry to a vendored tree. Both paths, same source.

## Nothing here depends on the server

That is the property this repository exists to hold. An engineer authoring a
tool schema compiles it, lints it and generates from it without running,
installing or cloning [`garmd`](../garmd). Enforcement at authoring time must
not require the thing that enforces at run time.

## What is deliberately not here

- **The governance chain.** Declaring that a tool requires clearance is this
  repository's business; enforcing it is [`garmd`](../garmd)'s.
- **Any taxonomy.** Compartments are an organisation's data-classification
  policy. This ships the mechanism — that compartments exist, that clearance
  compares, that a tool declares what it requires — never the vocabulary.
- **Commands that need the product.** `manifest`, `agent` and `tool index`
  depend on packages that live in [`garmd`](../garmd) today. They arrive here
  when their dependencies do, and not before — dragging the product across to
  keep a command would defeat the boundary above.

## Status

Not yet seeded. Intent recorded; the first cut is `init`, `gen`, `lint`, `new`
and the generator subcommands.
