# spec

**The contract language.** The proto annotations that describe a governed
tool, the compiler that reads them, and the rules that refuse a bad one.

Nothing here depends on the garm server. That is the point: an engineer
authoring a tool schema must be able to compile and lint it without running,
installing or cloning garm.

## What is here

| | |
|---|---|
| `proto/garm/v1/` | The annotations: verb, clearance, compartments, tool sets, effects, guidance, approval, audit. Published as a buf module, and vendorable into your own tree. |
| `cmd/garm/` | The CLI. Scaffolds a proto tree, runs the generators, lints, builds and checks catalogues. |
| `internal/toolgen/` | The reader, the emitter, and the lint rules. |
| `conformance/` | Golden proto sets and the errors they must produce. |

## Why the generator lives with the annotations

A new annotation and the code generation that reads it are one change. Split
across two repositories they become a release dance with a window in which the
annotation exists and nothing can compile it. One binary carrying both means
the two cannot skew — one version number instead of two that have to agree.

## What is deliberately not here

- **The governance chain.** Declaring that a tool requires clearance is this
  repository's business. Enforcing it is [`garm`](../garm)'s.
- **Any taxonomy.** Compartments are an organisation's data-classification
  policy. This repository ships the mechanism — that compartments exist, that
  clearance compares, that a tool declares what it requires — never the
  vocabulary. The engine ships the language, not the policies.
- **Tool implementations.** See [`tools`](../tools) and [`examples`](../examples).

## Status

Not yet seeded. Intent recorded; code arrives at Phase 3 of the split.

> **Open:** whether the CLI ships from here or from `garm`. Its centre of
> gravity is the contract language, which argues for here; it also carries
> product commands, which argues for there. To be settled before seeding.
