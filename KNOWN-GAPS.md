# Known gaps

What this command line tool does not do, and what it does on purpose that will
surprise you. Not an inventory of what works — the code says that, and a file
that repeats it goes stale in a way the code cannot.

Gaps in the contract itself are in
[`garm-ai/contracts`](https://github.com/garm-ai/contracts)'s own
`KNOWN-GAPS.md`, and gaps in enforcement are `garmd`'s.

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

- **`catalogue publish` does one PUT per object.** No multipart, no retry, no
  concurrency. A prompt is a markdown file and a catalogue is single-digit
  megabytes at a realistic size (see `docs/catalogue.md`), so none of the three
  earns its complexity yet. The test double covers HEAD and PUT and nothing
  else, deliberately: what is worth asserting is the order and the refusals,
  not an S3 implementation.
