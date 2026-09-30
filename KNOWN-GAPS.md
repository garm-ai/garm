# Known gaps

What this command line tool does not do, and what it does on purpose that will
surprise you. Not an inventory of what works — the code says that, and a file
that repeats it goes stale in a way the code cannot.

Gaps in the contract itself are in
[`garm-ai/contracts`](https://github.com/garm-ai/contracts)'s own
`KNOWN-GAPS.md`, and gaps in enforcement are `garmd`'s.

## What the manifest does not do yet

`catalogue build` composes from `catalogue.yaml` since v0.20.0. Four things the
design asks of it are not here, in the order they land.

- **`garm catalogue init` does not exist.** Every deployment in the estate is in
  the pre-manifest state — adopted packages copied into `proto/`, a matching
  `exclude_paths` entry, a drift gate watching the copy — and the manifest that
  describes such a tree has to be written by hand today. Its real job is
  migration rather than scaffolding: read the tree, write a `path:` entry and a
  `module:` entry for each copied package it can match to a requirement in
  `go.mod`, and **report rather than guess** where it cannot, because a copied
  package `go.mod` does not require is a tree compiling against descriptors it
  does not depend on, which is a defect and not an entry to invent.
- **The required-platform-package rule is not enforced.** A declaration that says
  `approval { mode: MODE_GRANT }` or declares a task of `Kind.ASK` needs
  `garm.tasks.v1` in the same catalogue, or the call parks on a task nobody can
  open and the only symptom is an absence. The manifest is what makes that
  checkable — the whole assembled set is visible here where the protoc plugin
  sees one directory — and it is unwritten. It lands with the adoption it
  refuses: the rule alone would break the bank's current catalogue, which is the
  rule working and still a broken build.
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
- **`garm lint` still takes one directory.** Only `catalogue build` composes. The
  rules that need the whole assembled set — A3's allowlist, and the required
  platform package when it lands — therefore see a manifest's inputs from the
  builder and never from the linter, so `garm lint` over a composed tree checks
  the deployment's own protos and not what it adopts. Lint learns the manifest
  with the rule that needs it.
- **`--proto` is deprecated in its help text and warns nothing at run time.** A
  warning on stderr would change the output of every pipeline that still passes
  it, which is all of them, so the deprecation is documentation until the flag is
  removed.

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

- **Prompt verification is still in the command package.** `catalogue build`'s
  assembly moved to `internal/catalogue` in v0.19.1 under the rule in
  `CLAUDE.md` — the package decides, the command prints — and `publish` did not
  follow it. `verifyPrompts` already takes values and returns values, so nothing
  about it is untestable where it sits, and its next change is not cosmetic: a
  composed catalogue resolves each prompt against the root of the *input* that
  contributed its agent, so the one `--prompts-root` becomes a root per resolved
  input. `manifest.Input` is that type as of v0.20.0 and it carries the entry's
  `prompts:` key, so the move is unblocked and not done: `publish` re-reads the
  manifest for nothing today and resolves every prompt against one root.
- **`catalogue publish` does one PUT per object.** No multipart, no retry, no
  concurrency. A prompt is a markdown file and a catalogue is single-digit
  megabytes at a realistic size (see `docs/catalogue.md`), so none of the three
  earns its complexity yet. The test double covers HEAD and PUT and nothing
  else, deliberately: what is worth asserting is the order and the refusals,
  not an S3 implementation.
