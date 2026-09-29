# The catalogue

**A catalogue is the artifact that says which tools a daemon serves.**

`garmd` does not know its tools at build time. It loads a catalogue at boot and
serves exactly what that artifact declares. Adding a tool is a catalogue
rebuild and a restart — not a release of the governance binary.

```
your .proto  ──garm catalogue build──▶  catalogue.binpb  ──▶  garmd
  declarations                          descriptors +            serves
                                        taxonomy +               what it
                                        provenance               declares
```

## Why it exists

The obvious reason is that recompiling a governance binary to add a tool does
not scale past one team. Every tool author waits on the platform's release
train, and the platform owns a queue of changes it has no opinion about.

The less obvious reason is that it makes the two things versionable
separately. A daemon ships on its own cadence and a catalogue ships on the
tool authors'. Neither blocks the other, and the compatibility between them is
a stated range rather than an accident of what happened to be built together.

## What it costs

Measured, not estimated. The benchmark is committed:

```console
$ go test ./cmd/garm/ -bench=Catalogue -benchtime=1x -run=XXX
```

| tools | artifact | build | load | retained heap |
|---|---|---|---|---|
| 100 | 53 KB | 14 ms | 1.5 ms | 2.4 MB |
| 1,000 | 378 KB | 133 ms | 14 ms | 10.7 MB |
| 10,000 | 3.7 MB | 969 ms | 112 ms | 93.5 MB |

Linear throughout: about **384 bytes per tool on disk** and **9.3 KB per tool
retained**. Figures from an Apple Silicon laptop; the shape of the curve is the
portable part, not the constants.

**Three separate costs, paid by different people.** Build is paid once by
whoever publishes. Load is paid at every daemon start. Retained heap is paid
for as long as the process runs, and it is the one that decides whether a
catalogue of a given size belongs beside every pod.

Load is almost entirely protobuf building and cross-resolving the registry.
Reading the annotations off it afterwards is under 2 ms at 10,000 tools, so
there is nothing to optimise on garm's side of that line.

The benchmark protos are deliberately undocumented. Comments live in
`field_docs`, and their size is a property of the schema rather than of the
catalogue — so these numbers are a floor, and a well-documented catalogue both
carries more and saves more from the strip below.

### Comments are stripped, prose is not

`SourceCodeInfo` carries spans and paths for every token in every file, and it
is roughly **half** of both numbers above — before the strip, 10,000 tools cost
9.7 MB on disk and 180 MB retained. Re-measure by deleting the strip in
`runCatalogueBuild` and running the benchmark again.

So the prose is lifted into `field_docs` at build time and `SourceCodeInfo` is
dropped. The schema keeps its documentation and the artifact stops carrying
the position of every brace.

**The schemas themselves are deliberately NOT precomputed.** They are projected
per principal — a field a caller may not read is *absent*, not marked — so
there is one schema per `(tool, clearance, compartments)` rather than one per
tool, and that space cannot be enumerated at build time. The only schema that
could be baked is the unprojected one, which is exactly the one that must never
be served. Projection assembles a schema at run time from the descriptor plus
`field_docs`, and caches by shape.

### Sizing is per deployment, not per organisation

93 MB is nothing for a central deployment and a great deal for a sidecar
running beside every pod. Nothing requires one catalogue to hold every tool:
**a deployment loads the catalogue it serves.** A pod whose agents use fifty
tools loads a fifty-tool catalogue and pays about half a megabyte.

For calibration, 10,000 is a stress case. A few hundred tools — a realistic
catalogue — costs single-digit megabytes.

## Starting from nothing

```console
$ garm init .
wrote third_party/proto/garm/tool/v1/tool.proto
wrote third_party/proto/garm/agent/v1/agent.proto
wrote third_party/proto/garm/card/v1/card.proto
wrote third_party/proto/garm/meta/v1/meta.proto
wrote buf.yaml
wrote buf.gen.yaml

$ go install github.com/garm-ai/garm/cmd/protoc-gen-garm-go@latest
$ # write proto/your/v1/service.proto, annotated
$ garm lint && garm gen && garm catalogue build -o catalogue.binpb
```

`garm gen` produces three things from one `.proto`: the messages, connect
handlers, and the **tool binding** — a typed Handler interface, a `Serve` that
registers it against a runtime, and the contract version and descriptor hash
the service advertises so a daemon can tell whether it is running the contract
the catalogue declares.

Two details in the scaffold that are not preferences:

**The vendored annotations go under `third_party/proto`, as their own buf
module.** Every module in a buf v2 workspace is an input, so annotations
living under `proto/` get Go generated for them — output that is never usable,
because the real one is in `garm/contracts` and two packages registering one
proto file panic at init.

**The binding is generated with `package_suffix`.** Colocated, it references
the connect Handler from its own connect sibling, and that sibling imports the
base package back for message types: a two-package import cycle nothing can
break from the outside.

## Building one

```console
$ garm catalogue build --proto proto -o catalogue.binpb --source "acme/tools@$(git rev-parse --short HEAD)"
wrote catalogue.binpb
  3 tool(s), 5 file(s), schema v1
  digest sha256:65a16d19cec0fa3c…
    acme.accounts.v1.get_balance
    acme.accounts.v1.list_accounts
    acme.calc.v1.add
```

**It lints first and refuses on any error.** A catalogue that does not lint
cannot be built. The alternative is an artifact that fails later at the
daemon's mount check — where the author is not present, and the failure is an
outage rather than a build error.

### Prompts are resolved beside the proto tree

An agent's `prompts.*.path` is relative to the directory that **contains** the
proto tree — the parent of `--proto`, which for the default `--proto proto` is
the directory you are standing in. A repository laid out as

```
proto/bank/agents/v1/support_assistant.proto
prompts/support-assistant.md
```

declares `path: "prompts/support-assistant.md"` and needs no flag.
`--prompts-root DIR` overrides it. `garm catalogue publish` takes the same
flag, defaulting to the working directory — the same place.

The path may not leave that root: an absolute path, or one that climbs out with
`..`, is a build error rather than a file that gets read and hashed. The
`sha256` is lowercase hex with no `sha256:` prefix, because it is compared
against a digest computed at load time and it is also the object key the prompt
is published under.

It also refuses to write a catalogue with no tools in it. A daemon started on
an empty catalogue serves nothing, and that is never what anyone meant.

No `buf` required. No plugins, no registry, no network.

## Publishing one

```console
$ garm catalogue publish catalogue.binpb s3://garm/catalogue/
wrote s3://garm/catalogue/prompts/d43a2fec89c3b32917f3550b916cc6751f6d326e25f9aa19089cf70dbfd2615a.md
wrote s3://garm/catalogue/catalogue.binpb
  sha256:65a16d19cec0fa3c…
```

**Prompts first, catalogue last.** A daemon and a runner poll the catalogue
object and load whatever is there. A catalogue visible before the files it
pins is a generation that cannot be loaded — and the reader cannot tell that
from a prompt that was tampered with. The order is the command.

Every prompt is read and hashed **before** the first byte is uploaded, so a
missing or drifted prompt publishes nothing at all. An object that already
exists under a prompt's key is left alone: a prompt is addressed by its own
sha256, so one that is there is already the right bytes.

The catalogue object is overwritten in place and the S3 ETag is the change
signal. There is no separate notification.

Credentials come from the AWS SDK's default chain. `AWS_ENDPOINT_URL` points
it at something else — SeaweedFS locally, MinIO, anything speaking S3 — and
switches it to path-style addressing, which is what those stores serve.

**Permissions.** The credential needs `s3:PutObject` and `s3:ListBucket` on
the destination bucket. The second is easy to miss: AWS answers a `HEAD` on a
key that is genuinely missing with 404 only when the caller can list the
bucket; without `s3:ListBucket` it answers 403 instead, which looks nothing
like "not there" and which `publish` — correctly — treats as a hard failure
rather than a green light to upload. A credential with `PutObject` alone can
build a catalogue that never publishes anything.

## Loading one

A daemon fails closed, and in a specific way:

- **An unresolvable import** is a startup failure. The set was meant to be
  self-contained.
- **A schema version outside the window** is a startup failure naming both
  versions.
- **Never a partial load.** Serving the part of a policy document one
  understands fails open by construction — and the annotations a binary cannot
  parse are disproportionately the *new* ones, which are the ones that
  restrict.

### Compatibility: N-2

A daemon reads the current annotation schema version and the two previous.

```
garmd understands schema v3 (current), v2, v1

  catalogue @ v3  → loads
  catalogue @ v1  → loads
  catalogue @ v0  → refused at boot
  catalogue @ v4  → refused at boot; upgrade garmd first
```

Backward is generous, so upgrading a daemon rebuilds nothing. Forward is always
closed, so adopting a new annotation means upgrading the daemon first, in that
order, deliberately.

**The check is scoped to `garm.tool.v1` and ignores every other extension
namespace.** A catalogue may carry declarations annotated in other
vocabularies — an agent runner's, for instance — and the daemon ignores them
entirely. Widening the check would refuse such a catalogue at boot, which is
knowledge of agents acquired through an error message.

`garm.agent.v1` is the namespace that paragraph anticipated. A service carrying
`(garm.agent.v1.agent)` declares an agent: the model, the bounds, the prompts
and the tools it may call. A daemon serving the catalogue sees only the two
governed tools that service declares — `Invoke` and `GetRun` — and never reads
the annotation. The runner reads it. Lint rules A1–A5 check it at build time,
so neither side discovers a bad manifest at load time.

### Cards and owners

Two more namespaces the daemon never reads, vendored by `garm init` beside the
other two and carried in the catalogue like any other file.

**`garm.card.v1`** is the vocabulary an inbox renders — a `Card` is one task,
run or agent input projected for one viewer at one moment — and the templates
an author may declare for it:

| Annotation | Number | On | Declares |
|---|---|---|---|
| `(garm.card.v1.task_card)` | 50202 | a `MODE_GRANT` tool's method | the open approval's title and body, with `{field}` references to the tool's `approval.material_fields` |
| `(garm.card.v1.result_card)` | 50201 | an agent service | the run card's title and body; literal text only in this release |
| `(garm.card.v1.card_role)` | 50203 | a field of a query's response | declared for §4 queries; nothing reads it yet |

A template contains no inputs, queries or actions: inputs come from the
decision message, actions are fixed by the card's kind. `Template.context` is
declared but not checked in this release.

**`garm.meta.v1`** is ownership — who is answerable for the thing a card
shows. `(garm.meta.v1.owner)` (50301) on a tool service or an agent names a
`team` (the unit a ledger row will be charged to), a `contact` and an
optional `on_call`; `(garm.meta.v1.method_owner)` (50302) overrides it for
the rare method owned elsewhere. Metadata, not policy: it lives beside the
card annotations rather than inside `garm.tool.v1`, and `ListTools` does not
carry it.

Neither namespace is read by `garmd`, and neither moves the schema version:
the check above is scoped to `garm.tool.v1` and a catalogue annotated with
both loads on a daemon that has never heard of them. agentd reads them from
the catalogue generation the task or run pinned and puts the owner on every
card it builds.

Two lint rules run where A1–A5 run, in `garm lint` and `garm catalogue build`:

- **C1** (error) — every `{field}` a `task_card` references is one of the
  tool's `approval.material_fields`, because the task stores those and
  nothing else of the request; a `result_card` references nothing at all,
  because agents cannot declare an output type yet and there is no field for
  a reference to resolve against. `context.<field>` references are left to a
  later rule (C7) and are unchecked.
- **O1** — every service with a `(garm.tool.v1.tool)` method or a
  `(garm.agent.v1.agent)` option carries an `owner` with a non-empty `team`.
  **A warning in v0.15.0, not an error**: no catalogue built before this
  release names an owner, and a build gate nobody can pass on the day it
  appears is a gate people disable. It becomes an error in a later release;
  add owners now and the upgrade is a no-op.

### Runner-supplied request fields

A request field can belong to the runner rather than to whoever is asking.
`source: SOURCE_RUNNER` on its field policy says so:

```proto
string idempotency_key = 6 [(garm.tool.v1.field_policy) = {
  read: CLEARANCE_PUBLIC on_deny: { mask: {} } source: SOURCE_RUNNER
}];
```

Why this exists: an idempotency key chosen by a model is not idempotency. A
model that retries after a timeout it never saw the answer to picks a new key
and pays twice, or reuses one for two different payments and pays once. The
key must come from the thing that knows what a retry is, and that is the
runner's durable workflow — never the model, and never a caller typing a
request by hand.

What each side does with the mark:

- **garmd** projects a `SOURCE_RUNNER` field out of the schema `ListTools`
  hands to any caller — it is not the caller's to fill — and refuses a
  request that sets one unless the call's `Garm-Invocation` carries an `exec`
  runner identity. That is garmd's half, filed for **garmd v0.2.2**; a daemon
  older than that ignores the field, which is what lets the annotation land
  without moving the schema version (see below).
- **agentd** strips every `SOURCE_RUNNER` field from the schema the model is
  shown and fills each by rule at dispatch. The one rule this release knows:
  `idempotency_key` = `<run_id>-<dispatch seq>` — identical on the granted
  retry of a parked call, distinct for a second call in the same run.

And the lint rule that keeps the two honest:

- **L34** (error) — a `SOURCE_RUNNER` field must be a top-level string of
  the request message named `idempotency_key`, the only runner rule this
  release knows. A runner field with any other name is one the model cannot
  see, the caller may not set and the runner has nothing to put in — a
  request nobody can send. `source` on a response field, on a nested field,
  or on a message's `default_field_policy` is refused for the same reason,
  each with its own sentence. `SOURCE_UNSPECIFIED` — the default — is the
  caller, and L34 never looks at a caller field whatever it is called.

**The schema version did not move.** `source` is an additive field an older
daemon reads as unknown bytes and ignores: every policy it already knew —
`read`, `on_deny`, `compartments` — decodes exactly as before, and the
catalogue still stamps **schema v1**. `cmd/garm`'s
`TestASourceRunnerFieldLoadsOnASchemaV1Daemon` proves it by decoding a marked
field through the v0.15.0 descriptor. The cost of not moving it is stated
above: a daemon before v0.2.2 serves the field to callers and does not refuse
one who sets it. Until then agentd's strip-and-overwrite is the enforcement,
and it is tested there.

## Who builds one

Whoever owns the tools. There is no central catalogue.

An organisation's catalogue is its own tools, plus any tool packages it chose
to adopt, plus its own taxonomy — built into one self-contained artifact with
one digest. Not a base plus an overlay: merge semantics are a governance
surface, and *which layer wins* is a question nobody should have to ask about a
policy document.

Where two sources declare the same compartment, identical declarations merge
and any difference fails the build, naming both:

```
ERROR: compartment "financial" declared two ways
  garm-ai/tools/payments@v1.2
  acme/proto/billing/v1/billing.proto:14
Adopt one definition or rename yours.
```

One decision, made once, yielding a coherent model — rather than a silent
resolution nobody reviewed.
