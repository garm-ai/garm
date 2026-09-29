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
- **Cards and owners are the demo slice, not the design.** `garm.card.v1`
  and `garm.meta.v1` land whole (studio cards design §1.1, §3.2, §3.5), but
  of the lint rules only C1 (with C3 folded in) and O1 exist. Not written:
  C2 (`Input.id` against the decision message), C4 and C6 (queries and
  `card_role`), C5 (a wall-of-facts warning), C7 (`Template.context` and
  `context.<field>` references — C1 skips those references rather than
  refusing them), O2 (team format) and O3 (`method_owner` in use). A
  `result_card` may carry literal text only until agents declare an output
  type (program plan §7 item 17); C1 says so when a reference appears. O1 is
  a **warning** in v0.15.0 so existing catalogues still build; it is meant to
  become an error once they carry owners.
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
