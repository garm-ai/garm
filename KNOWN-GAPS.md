# Known gaps

What is deliberately missing from this repository, and where it went. Copied
from the private monorepo at commit `bc81bf0`.

## Left behind, on purpose

**The generated tool registry fixture.** `policy/testdata/testdatagarm` is
generated in the monorepo and carries `Tools`, `Mount`,
`RegisterTestService` and a NATS client — all of which import the enforcing
package. The tests here only ever used `Compartments`, so that is all this
holds, hand-written and byte-identical to what the plugin emits.

*Where it goes:* `garmd`, beside the code it registers into. The right fix
is arguably to move `ToolDef` out of the enforcing package and into the
contract, since a declaration is not an enforcement — decide that when
`garmd` is seeded.

**One case of `TestConnectNamingMirror`.** It compared `ConnectNames`
against real connect-go output for two services. The contract declares no
services, so only the fixture's `TestService` case survives here. The
`ControlService` case belongs in `garmd`.

## Not built yet

- `garm new toolservice` is unwritten. `init`, `gen`, `lint`, `claims check`
  and the `catalogue` subcommands exist.
- `protoc-gen-garm-python` does not exist. Go only.
- The annotations are not published as a buf module.
- **`output_rules` are parsed, not evaluated.** A4 checks that each
  `output_rules[].expr` parses as CEL and nothing more. What variables an
  output rule sees is not fixed by any design document yet, so type-checking
  one here would invent that contract in a lint rule. The repair loop that
  would evaluate them is out of scope for the agent MVP
  (`spec/…/2026-09-28-agent-mvp-design.md`, Scope: Out).
- **Not every card lint rule exists.** C1 (with C3 folded in), C8 and C9 do;
  O1 does. Not written: C2 (`Input.id` against the decision message), C4 and
  C6 (queries and `card_role`), C5 (a wall-of-facts warning), C7
  (`Template.context` and `context.<field>` references — C1 skips those
  references rather than refusing them), O2 (team format) and O3
  (`method_owner` in use). A `result_card` may carry literal text only until
  agents declare an output type (program plan §7 item 17); C1 says so when a
  reference appears. O1 is a **warning** since v0.15.0 so existing catalogues
  still build; it is meant to become an error once they carry owners.

- **C8 sees a template, not a card.** A Section is a floor and a child may
  not be labelled below it, and the one place that structure is DECLARED is a
  `task_card` or `result_card` template — so that is the one place a build
  can refuse it. Labels mostly travel on the VALUE, on a card built at run
  time by a generated default or an override, and no linter reaches those.
  The daemon enforces the same shape there, per call, as it enforces the
  endpoint floor.
- **`source: SOURCE_RUNNER` is declared here and enforced by agentd; garmd's
  half is not built.** `FieldPolicy.source` (v0.16.0) says a request field
  is the runner's, and lint L34 holds it to the one rule a runner knows
  (`idempotency_key`). agentd strips and fills. What garmd is meant to do —
  project the field out of `ListTools` and refuse a caller that sets one
  without an `exec` runner identity on `Garm-Invocation` — is filed for
  **garmd v0.2.2**. Until then a daemon serves the field to every caller and
  the runner's overwrite is the only enforcement. The annotation schema
  stayed at v1 so that a v0.2.0 daemon still mounts a catalogue carrying the
  field; `docs/catalogue.md` says what that costs.
- **`audience` is declared here and enforced nowhere yet.**
  `ToolPolicy.audience` (v0.17.0) says whether a tool is for a model, a
  person or the runner, and lint holds a manifest to it (A9) and a grant-mode
  tool's approval card to it (A10). What garmd is meant to do — filter
  `ListTools` by the audience the caller asks for, defaulting to `AGENT` — is
  **not built**, and neither is a person's client that asks for `PERSON`.
  Until both land, a `PERSON` tool is still offered to a model by a daemon
  that has not learned the field, and the allowlist (A9, plus the runner's
  own filter) is what actually keeps a card out of a model's schema. The
  annotation schema stayed at v1 so that an older daemon still mounts a
  catalogue carrying the field, on the same reasoning as `source` above.

- **The generated result card has no answer to show.** A card about an answer
  needs the answer, and the generated wrapper has no way to reach a response
  the handler returned to somebody else. The runtime is meant to seal each
  call's response under its call id and hand it back; until that store exists
  the default answers `result_unavailable` and only a tool that keeps its own
  record has a result card — it overrides `ResultCard` and calls the generated
  `<Method>ResultCardFrom` with its own row.

- **The generated cards leave repeated fields out.** An input card has no
  control for a list and a result card has no layout for one, and the wrong
  layout is worse than an override. The design's "a FactSet per repeated
  message field" is not built; a result card's facts are the scalar leaves
  reachable without crossing a repeated field, to a depth of two. An author
  with a list overrides.

- **The card defaults are emitted for `emit=toolsdk` only.** That is what a
  tool service registers against. `emit=server` is garm's own wiring for the
  daemon's side and mounts from the catalogue, which already carries the card
  methods.

- **`contracts/grants` verifies a grant; it does not spend one.** The shared
  half of grant verification — parse, verify the signature against a supplied
  key set, read the claims (including `task`), compare a material map or a
  request against the digest — lives in `contracts/grants` so that garmd and
  a tool service check the same things the same way. What stays with each
  caller: the replay cache that makes a grant single-use, the issuer and
  audience allowlists, and the refusal shaping that decides what a surface
  answers and when an operator is paged. A caller that verifies a grant and
  does not record its `jti` has enforced everything except single use, which
  is the one property this package cannot hold for it.

- **`catalogue publish` does one PUT per object.** No multipart, no retry, no
  concurrency. A prompt is a markdown file and a catalogue is single-digit
  megabytes at a realistic size (see `docs/catalogue.md`), so none of the three
  earns its complexity yet. The test double it is tested against covers HEAD
  and PUT and nothing else, deliberately: what is worth asserting here is the
  ORDER and the refusals, not an S3 implementation.

## Deferred with their dependencies

`manifest`, `agent` and `tool index` are commands of the monorepo's
`garmdev` that need `agentmanifest`, `generation/policy` and
`toolplane`. They arrive here only if those dependencies are extracted, and
dragging the product across to keep a command would defeat the boundary in
`CLAUDE.md`.

- **The protoc plugin does not resolve an agent's allowlist.** buf invokes
  `protoc-gen-garm-go` once per directory, so the tools an agent names usually
  live outside the request. The plugin warns (A3) that the allowlist and its
  guards were not checked and names the commands that do: `garm lint` over the
  proto tree and `garm catalogue build`, which refuses on any A-rule error.
