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
- **The taxonomy is the deployment's since v0.22.0, and nothing migrates a tree
  to it.** `catalogue.yaml` takes a `taxonomy:` key with `compartments:` and
  `tool_sets:`, each entry a name and a required description; lint resolves every
  name a tool REQUIRES against it and refuses an undeclared one. A tree that
  declares the key stops reading file-level proto options altogether — a union
  would preserve the very thing the key exists to end, which is that adopting a
  tool silently extends the vocabulary governing who may see what. A tree with no
  `taxonomy:` key keeps the scrape exactly, which is every catalogue in the estate
  today including the one the plane runs.

  What is missing is the migration. `catalogue init` writes a manifest for a
  pre-manifest tree and does **not** write a `taxonomy:` block, so moving a
  deployment over means transcribing its `option (garm.tool.v1.compartments)`
  declarations into YAML by hand and then deleting the protos that carried them.
  `init` already scrapes the tree for everything else it writes and this is the
  obvious next entry in its report; it is not built.

  Two consequences worth knowing. **`--proto` can never declare a taxonomy**: the
  flag synthesises a one-entry manifest in memory, so a tree using the deprecated
  shorthand always gets the scrape. And **`garm.tool.v1.compartments` and
  `tool_sets` are not deprecated in the contract** — the extensions still exist
  and `garm init` still vendors them, because a tool package genuinely does need
  to document the words it requires somewhere a reader can find them. What changed
  is who those declarations bind: a deployment, not every deployment that adopts
  the package.
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
- **`garm lint --proto` is now WRONG on a composed tree, not merely narrower, and
  the fix is decided and unbuilt.** `--proto` becomes a synthetic one-entry
  manifest and then runs the FULL rule set, `PartialSet` unset. On a tree that
  adopts a tool from a module the adopted tool is not in that directory, so A3
  refuses an agent's allowlist for naming a tool it cannot see — the bank's
  research assistant fails on `web.v1.fetch_page` while the manifest passes. A
  deprecated flag that produces a false failure is worse than one that is
  removed.

  The decision, for whoever picks it up: **refuse, when a `catalogue.yaml` sits
  beside it.** `--manifest` together with `--proto` is already an error naming
  "two different inputs", and `--proto` in a tree that has a manifest is the same
  mistake made implicitly — the tree has said what composes into its catalogue,
  and the flag asks for part of it to be judged as the whole. `--proto` in a tree
  with no manifest is still the whole catalogue and keeps today's behaviour, so
  the pre-manifest pipelines it exists for are untouched. What it is NOT is
  `PartialSet`: downgrading A3, A9 and P1 to warnings whenever `--proto` appears
  would quietly stop checking them for every tree that has not migrated, which is
  most of them, and a rule skipped wherever nobody is looking is not a rule.

  Held out of v0.21.1 deliberately. It adds a refusal where an invocation
  succeeds today, which is a breaking change for every pipeline still passing the
  flag, and it should not ride along with a fix people may want to cherry-pick.
  And the two failures are not the same danger: this one is loud and its
  workaround is the documented replacement, where the `sets` miss was silent and
  reassuring.
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
- **The plugin no longer refuses an undeclared compartment or tool set, and that
  is a real loss.** L7's reference half and L20 joined A3, A9 and P1 in the group
  `PartialSet` turns into warnings when the taxonomy moved into the manifest in
  v0.22.0: buf invokes the plugin once per directory with a descriptor set and no
  manifest, so it cannot tell a name declared in `catalogue.yaml` from one that is
  declared nowhere. Before the move, a tree whose taxonomy proto sat beside its
  tools had a typo caught by `garm gen`; it no longer will, and `garm lint` and
  `garm catalogue build` still do. What the old behaviour cost was worse — a
  deployment could not delete its COPY of an adopted taxonomy proto, because the
  plugin refused the tool naming a set the copy declared, while `catalogue build`
  was perfectly content. A rule that forces a tree to keep a copy in order to pass
  is a rule enforcing the thing the manifest exists to remove.
- **The plugin does not resolve an agent's allowlist.** buf invokes
  `protoc-gen-garm-go` once per directory, so the tools an agent names usually
  live outside the request. The plugin warns (A3) that the allowlist and its
  guards were not checked and names what does check them: `garm lint` over the
  tree, and `garm catalogue build`. A7 warns the same way, per step whose tool
  is absent, for the parts that need that tool's descriptors — `with` keys
  against its request, `set` expressions against its response, and which of its
  request fields are the runner's. What A7 still ERRORS on there is everything
  answerable from the agent's own file: the allowlist, `initial`, `set` keys
  against the state message, edge predicates, and the write-dominator rule
  itself, because the reads are found by parsing and a parse needs no
  descriptor.

## What `catalogue diff` does not compare

`catalogue diff` prints "no policy changes." when it finds none, and a reviewer
acts on that sentence, so the sentence has to be bounded. A missing rule in the
linter fails open and somebody eventually notices what it did not refuse; a
missing comparison here produces a **reassurance**, which is worse than having no
tool.

It went wrong exactly once, and v0.21.1 is the fix. `sets` was never read, so the
contract bump that gave `garm.tasks.v1.decide_task` and `approval_card`
`sets: ["triage"]` — two named approvers going from `not_found` to able to decide
— reported nothing at all. Tool-set membership is the dimension that decides
*reachability*: garmd refuses a scoped session any tool that shares none of its
sets, so adding or removing one grants or revokes access with no clearance and no
compartment moved.

Every field of `garm.tool.v1.ToolPolicy` and `FieldPolicy` was then walked
against what an engine actually reads. `internal/policydiff/coverage_test.go`
holds the result as a test: each field is compared or on an ignore list **with the
reason beside it**, "compared" is proved by moving the field and requiring a
change, and every comparison must speak in **both** directions. The shape is
copied from `garm-ai/contracts`' tool-set rule and `garm-ai/sink`'s lake columns.

What that walk found, besides `sets`: `approval.material_fields`,
`approver_min_clearance`, `approver_compartments` and `max_grant_age_seconds`
were uncompared, and so were `FieldPolicy.write` and `FieldPolicy.source`. All
six are compared now. Three comparisons were one-sided — `audit.level` and
`audit.retain_days` reported only a drop, `audit.fail_closed` only its removal —
so every narrowing of those fields was invisible. Both directions now report.

**The six deliberate omissions.** Each is a claim that a change to the field is
not a policy change, or that it is already reported by another route:

- **`name`** is half the FQN the diff keys tools by, so a rename already reports
  as the old tool removed and a new one added — which is what a rename IS to a
  grant naming the old FQN and to a manifest pinning it.
- **`exclude`** is not a tool with different policy, it is a tool that does not
  exist: `compiler.Tools` drops an excluded method, so it reports as removed.
- **`title` and `description`** are prose. Published to callers, read by nothing
  that decides. A description is model-facing text and worth reviewing, and `git
  diff` shows it; putting it here would bury the lines that need a human.
- **`guidance`** is prose for a model. Three fields reach callers, and `examples`
  is dropped before it reaches anyone.
- **`effects`** is advisory at run time. `idempotent`, `reversibility`,
  `external` and `compensating_tool` are projected into garmd's definition and
  compared for declaration identity, and no invoke branches on them. What acts on
  them is `garm lint` at build time, and a refused tree is a better report than a
  diff line.
- **`audit.record_request` / `record_response`** make the tool **unmountable**:
  garmd refuses it by name, because a ledger `Event` carries no payload to put
  them in. A change is not a change to what is recorded; it is a tool that will
  not start.
- **The inside of an `authorization` block.** Only its presence is compared,
  because only its presence is read: garmd projects the annotation down to one
  `HasAuthorization` bool, and the relation, object type and field selectors
  reach no daemon at all — there is no production FGA checker in the estate. When
  one ships, the comparison follows.

**`audience` is reported as `unclear`, on purpose.** It decides who a tool is
*offered* to and is never consulted by the predicate that admits an invoke, so
widening it grants nobody anything they could not already call and narrowing it
hides a tool a caller may still name. It is reported at all because mistaking it
for a gate is how `decide_task` became unreachable: it declared
`audience: [AUDIENCE_PERSON]` and no set, written as though the audience were the
scoping.

**Two things it still cannot see, both outside this repository.**

- **A tool's descriptors can move without the catalogue's declared version
  moving.** For the four packages this binary links — `garm.tasks.v1`,
  `garm.agent.v1`, `garm.card.v1`, `garm.meta.v1` — the descriptors in a built
  catalogue come from **this binary's** `contracts` pin rather than from the
  version a manifest entry names (see "A proto package this binary LINKS" above).
  Build the same tree with `version: v0.4.0` and with `v0.5.0` and the artifacts
  carry identical `garm.tasks.v1` policy. The diff is correct about the artifacts
  it is handed; what a reader must not conclude is that a manifest bump with no
  diff output changed nothing. Upgrading the CLI is the change that moves those
  four packages, and diffing two catalogues built by two CLI versions is how you
  see it.
- **Shape changes are absent by design.** `buf breaking` covers those, and mixing
  them in would bury the handful of lines that need a human.

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
