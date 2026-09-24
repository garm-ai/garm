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

- `cmd/garm` is the protoc plugin's `main`, not a cobra CLI. `init`,
  `gen`, `lint`, `new` and the `protoc-gen-garm-*` subcommands are the
  first cut and are unwritten.
- `protoc-gen-garm-python` does not exist. Go only.
- The annotations are not published as a buf module.

## Deferred with their dependencies

`manifest`, `agent` and `tool index` are commands of the monorepo's
`garmdev` that need `agentmanifest`, `generation/policy` and
`toolplane`. They arrive here only if those dependencies are extracted, and
dragging the product across to keep a command would defeat the boundary in
`CLAUDE.md`.
