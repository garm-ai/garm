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
