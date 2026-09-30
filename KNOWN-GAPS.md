# Known gaps

What this command line tool does not do, and what it does on purpose that will
surprise you. Not an inventory of what works — the code says that, and a file
that repeats it goes stale in a way the code cannot.

Gaps in the contract itself are in
[`garm-ai/contracts`](https://github.com/garm-ai/contracts)'s own
`KNOWN-GAPS.md`, and gaps in enforcement are `garmd`'s.

## What the manifest does not do yet

`catalogue build` composes from `catalogue.yaml` since v0.20.0; `catalogue init`
writes one, `garm lint` reads one and P1 refuses a catalogue with no queue for the
approvals it promises since v0.21.0. What the design still asks for, and the two
rows of §6's table P1 cannot check:

- **The `Kind.ASK` row of §6's table is not enforceable, and the design says it
  is.** §6.1 asserts that `MODE_GRANT` and `Kind.ASK` are both already
  annotations. Only the first is: `garm.tasks.v1.Kind.ASK` is the value of
  `CreateTaskRequest.kind`, set by the runner at run time, and the DECLARATION
  that was to carry it is the agent manifest's `asks` list — specified in the
  cards-and-tasks design and absent from `garm.agent.v1.AgentPolicy`, whose seven
  fields are mode, principal, model, bounds, prompts, tools and output_rules. So
  an agent that puts a question to a person is not held to needing a queue to put
  it in. It becomes one more row in `internal/compiler.requirementsOf` when the
  contract grows `asks`.
- **The artefact row of that table is specified and unenforceable.** "Produces an
  artefact reference" requires `garm.artefacts.v1`, and nothing in
  `garm.tool.v1` says a tool produces one. It needs a declaration to hang off —
  most likely a response field typed as an artefact reference, which the artefact
  store's own contract will introduce. Inferring one (a field named
  `artefact_id`, say) would be this linter guessing at governance, so P1 leaves
  it alone, and `garm init`'s scaffolded manifest does not name a module for it
  either, because neither the package nor `artefactd` exists yet.
- **`catalogue init` matches a copy to a module through its `go_package`, so a
  copy whose generated Go is not in the module graph reports as unpinned.** The
  copy's own `go_package` names an import path and `go list` maps that to a
  module, which is exact and is the go command's answer rather than a prefix
  search of the requirements. The case it cannot see is a module that is required
  and whose protos are there while the generated Go package at that import path
  is not — a proto-only module, or a tree whose Go moved. The report then says
  the tree requires no module providing that path, which is true of the path and
  not of the module. Nothing walks the prefixes, because that would be a second
  module resolver.
- **`catalogue init` writes the manifest and deletes nothing.** The copies, the
  `exclude_paths` entries and the drift gates that exist only because of them are
  named in the report and left on disk. Until the copies are gone the tree is in
  both states at once and `catalogue build` refuses it by name — two inputs
  declaring one proto package — which is that check working, and which means the
  written manifest describes the tree as it will be rather than as it is.
- **The taxonomy is still scraped from proto options.** `compartments` and
  `tool_sets` come from a file-level option unioned across every file in the set,
  first declaration winning, which means adopting a tool silently extends the
  vocabulary that governs who may see what. The manifest is where a deployment
  should declare it. Nothing in `catalogue.yaml` accepts a `taxonomy:` key yet,
  and an unknown key is refused, so a tree cannot pre-empt this.
- **A composed agent's prompts are not resolvable.** An entry's `prompts:` key is
  parsed and carried on `manifest.Input`, and consumed by nobody: `catalogue
  publish` still takes one `--prompts-root` and resolves every pinned digest
  against it. So an adopted agent whose prompts live in its own module cannot be
  published yet. See Publishing, below.

## How a module input resolves, and what that does not check

- **A module's protos are found by convention, with no way to say otherwise.**
  `<module>/proto` if it exists, else the module root, and a proto package at the
  directory its name spells. Every tree in this estate follows both, and a module
  that does not is refused with a message naming the directory it looked in —
  which is honest, and not the same as configurable. There is no `proto_root:`
  key, because no module has needed one.
- **A proto package this binary LINKS is compiled from the linked descriptor, not
  from the module cache.** `internal/compile`'s resolver answers from
  `protoregistry.GlobalFiles` before it falls through to source, which is what
  makes this binary's copy of an annotation authoritative — and it applies to
  `garm.tasks.v1` too, which is a service contract rather than a vocabulary. So
  for that one package a module input's recorded version describes **the tree's
  requirement**, while the descriptor bytes are the ones `garm` itself links. The
  two are normally the same tag and nothing warns when they are not. It is
  visible: `Provenance.compiler` and `producer` identify the binary, so a digest
  that does not reproduce is diagnosable, but the artifact does not state the
  disagreement.
- **`--proto` is deprecated in its help text and warns nothing at run time**, on
  `garm lint` as well as on `catalogue build`. A warning on stderr would change
  the output of every pipeline that still passes it, which is all of them, so the
  deprecation is documentation until the flag is removed.
- **A tree with no `go.mod` cannot pin anything, and only `catalogue init` says so
  kindly.** `Resolve` refuses a manifest with module entries when there is no
  go.mod in the manifest's directory or any directory above it — up, because a
  deployment is not always its own module and the bank's go.mod is at the root of
  `garm-ai/examples`. `catalogue init` reports the same state per copy instead of
  passing the go command's own wording through.

## Commands that do not exist

- **`garm new toolservice`** is unwritten.
- **`protoc-gen-garm-python`** does not exist. Go only, so a Python tool
  service cannot be generated at all.
- **`manifest`, `agent` and `tool index`** were commands of the monorepo's
  `garmdev` and need `agentmanifest`, `generation/policy` and `toolplane`.
  They arrive here only if those are extracted from `garmd`; dragging the
  product across to keep a command would defeat the boundary in `CLAUDE.md`.

## What lint does not check

- **`output_rules` are parsed, not evaluated.** A4 checks that each
  `output_rules[].expr` parses as CEL and nothing more. What variables an
  output rule sees is not fixed by any design document, so type-checking one
  here would invent that contract in a lint rule.
- **P1 is one row of §6's table, and the group is `L`, `A`, `C`, `O` and now
  `P`.** The letter is new because the rule judges neither an agent nor a card: a
  declaration that depends on a capability another PACKAGE provides must find that
  package in the same catalogue. New capabilities add rows to
  `requirementsOf`, not rules. Two rows are missing and both are above.
- **Six card and ownership rules are unwritten.** C1 (with C3 folded in), C8,
  C9 and O1 exist. Not written: C2 (`Input.id` against the decision message),
  C4 and C6 (queries and `card_role`), C5 (a wall-of-facts warning), C7
  (`Template.context` and `context.<field>` references — C1 *skips* those
  rather than refusing them), O2 (team format) and O3 (`method_owner` in use).
  O1 is a **warning**, not the error the design asks for, because no catalogue
  built before v0.15.0 names an owner.
- **C8 sees a template, not a card.** A Section is a floor and a child may not
  be labelled below it, and the one place that structure is *declared* is a
  `task_card` or `result_card` template. Labels mostly travel on the value, on
  a card built at run time, and no linter reaches those.
- **Lint is the authoring half only.** `source: SOURCE_RUNNER` (L34) and
  `audience` (A9, A10) are held to their rules here and enforced by nobody at
  run time yet; what garmd owes for each is in its own gaps file. A catalogue
  that passes every rule here can still be served by a daemon that ignores
  both fields.
- **The plugin does not resolve an agent's allowlist.** buf invokes
  `protoc-gen-garm-go` once per directory, so the tools an agent names usually
  live outside the request. The plugin warns (A3) that the allowlist and its
  guards were not checked and names what does check them: `garm lint` over the
  tree, and `garm catalogue build`.

## What the generator leaves to an author

- **The generated result card has no answer to show.** A card about an answer
  needs the answer, and the generated wrapper cannot reach a response the
  handler returned to somebody else. Until the runtime seals each call's
  response under its call id, the default answers `result_unavailable`; a tool
  that keeps its own record overrides the card's RPC name (`ResultCard`, or
  `<Method>ResultCard` on a service with more than one tool) and calls the
  generated builder, which is named `<Service><Card>From` — the Go identifier
  is qualified by the service where the RPC name is not; see
  `docs/catalogue.md`.
- **The generated cards leave repeated fields out.** No control for a list on
  an input card, no layout for one on a result card — the wrong layout is worse
  than an override. A result card's facts are the scalar leaves reachable
  without crossing a repeated field, to a depth of two.
- **Card defaults are emitted for `emit=toolsdk` only.** That is what a tool
  service registers against; `emit=server` mounts from the catalogue, which
  already carries the card methods.

## Around the contract dependency

- **Nothing checks that a consumer's vendored annotations match this binary.**
  `garm init` writes the contract's bytes and never looks again. The bytes are
  `embed`ed verbatim precisely so that a drift check could be a byte
  comparison, and that check is not written. It matters more since v0.18.0
  renamed the Go import paths: a tree that upgrades the CLI without
  `garm init --force` keeps generating imports of packages that no longer
  exist.
- **Nothing checks which version of the contract this binary pins.** It is a
  `require` line reviewed like any other. A `garm` release built against a
  stale contract lints against a stale vocabulary and says nothing about it.
- **Nothing here verifies a hand-written copy of generated output, because
  there is none left to verify.** `contracts/KNOWN-GAPS.md` used to name a
  check owed by garm — its `policy/testdata/testdatagarm` transcribed what
  `cmd/protoc-gen-garm-go` emits, and only this repository has the plugin.
  contracts v0.2.0 derives that value from the declaration instead, so the two
  cannot disagree and the check has nothing to compare. If a fixture there ever
  goes back to hand-writing generated output, this is the repository that owes
  the check, and nothing warns that it is owed.

## Publishing

- **Prompt verification is still in the command package, and a composed agent's
  prompts are still resolved against one root.** `catalogue build`'s assembly
  moved to `internal/catalogue` in v0.19.1 under the rule in `CLAUDE.md` — the
  package decides, the command prints — and `publish` did not follow it.
  `verifyPrompts` already takes values and returns values, so nothing about it is
  untestable where it sits, and its next change is not cosmetic: a composed
  catalogue resolves each prompt against the root of the *input* that contributed
  its agent, so the one `--prompts-root` becomes a root per resolved input.
  Nothing about that fell out of v0.21.0. `publish` reads a built
  **artifact** — not the manifest — and the artifact now records which module and
  version contributed each proto package, so the module's directory is reachable
  from it; what is not in the artifact is the entry's `prompts:` sub-path, which
  lives only in `catalogue.yaml`. So the work is a manifest read that `publish`
  does not do today, plus the move, and `manifest.Input` is the type it lands
  on.
- **`catalogue publish` does one PUT per object.** No multipart, no retry, no
  concurrency. A prompt is a markdown file and a catalogue is single-digit
  megabytes at a realistic size (see `docs/catalogue.md`), so none of the three
  earns its complexity yet. The test double covers HEAD and PUT and nothing
  else, deliberately: what is worth asserting is the order and the refusals,
  not an S3 implementation.
