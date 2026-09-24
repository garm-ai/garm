# garm — the CLI and the contract language

`garm` is the command line tool people type. It scaffolds a proto tree,
compiles governed tool declarations, refuses bad ones, and builds the
catalogue a runtime serves. It is **not** the server; that is `garmd`, in a
separate repository.

## The invariant that defines this repository

**Nothing here may depend on `garmd`.**

An engineer authoring a tool schema must be able to compile, lint and generate
without running, installing or cloning the server. Enforcement at authoring
time must not require the thing that enforces at run time.

Two consequences that look like bugs and are not:

- `internal/compiler` emits import paths for `garmd` packages
  (`toolplanePkg`). Those are `protogen.GoImportPath` **string constants** —
  paths written into generated code, not dependencies of this module. Keep
  them that way.
- `policy/testdata/testdatagarm` holds only the declared taxonomy. The
  generated registry that normally sits beside it imports the enforcing
  package, so it lives in `garmd`. See `KNOWN-GAPS.md`.

## What lives where

| | |
|---|---|
| `proto/garm/tool/v1/tool.proto` | The annotations. The whole contract this repository owns |
| `contracts/` | Separate Go module — what a tool author imports. Must not require the parent |
| `policy/` | Compiles field annotations into redaction plans. `garmd` imports this rather than reimplementing it: two implementations of plan compilation would be a governance bug |
| `internal/compiler/` | Loads protos, lints them, emits code. The plugin |
| `cmd/garm/` | The CLI |

## Naming, and why it is not what it was

Renamed on extraction from the private monorepo, where different neighbours
forced different names:

- `toolpolicy` → `policy`. The daemon's app-manifest policy stays in
  `garmd/generation/policy`, so the old collision dissolved with the split
  rather than needing a prefix.
- `internal/toolgen` → `internal/compiler`. Beside `policy`, "toolgen" did not
  say how the two differed. One compiles declarations to code; the other
  compiles declarations to runtime plans.
- Proto package `garm.v1` → `garm.tool.v1`. The annotations were one file
  among ten; the other nine were the daemon's service surface and went with
  it. A tool author now imports the annotations and never sees
  `GenerationService`.

## Working here

```
mise install        the toolchain
mise run gen        regenerate the contract and fixtures
mise run test       go test ./... -race
mise run lint       buf lint + go vet
mise run ci         what CI runs
```

`mise` is the task runner as well as the toolchain manager. Task names are the
same across every garm-ai repository — that consistency is the point.

## The design record is not in this repository

Specifications, decisions and plans live in the **private monorepo** at
`~/Documents/github/spikes/garm`, under `docs/superpowers/`. They are one
interlinked corpus — specs cite each other by section — so they were not split
across repositories, and they are not published.

Read them there when you need the reasoning. The ones that govern this
repository:

- `specs/2026-09-25-public-split-design.md` — why these repositories exist
- `decisions/2026-09-25-garm-is-the-cli.md` — why `garm` is the CLI
- `specs/2026-09-24-tool-service-shell-design.md` — the contract artifacts
- `specs/2026-09-24-call-stack-design.md` — what the annotations govern

**Do not create `docs/superpowers/` here.** This repository is public; that
record is not.
