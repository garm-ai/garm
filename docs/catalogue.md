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

**Every part of a card carries a label.** `Label{clearance, compartments}` on
an `Element`, a `Fact`, a `Choice` and on the `Card` itself says who may see
that part. The tool that serves the card sets it — the generated default from
the source field's policy, an override by hand — and the daemon drops what the
viewer does not reach when it projects the answer. Two things about an absent
label are worth saying plainly:

- **Absent is not public.** An unlabelled element takes the card endpoint's
  own policy, which is the parent tool's clearance and compartments. Forgetting
  to label is as tight as the endpoint, never looser.
- **Below the endpoint is an error, not a leak.** An element labelled beneath
  the endpoint's own policy fails the whole card rather than being served.

A `Section`'s label is the **floor** of everything inside it: a child may be
labelled higher, never lower (lint C8). A template says the same thing
declaratively — `TemplateSection.access` and `FactRef.access` — where an unset
`access` means the source field's own policy, so an author writes one only to
make something *tighter*.

`Fact.field` records the dotted path a fact's value came from, when it came
from a field at all. That is what lets a client rebuild the material map for
an approval and what the daemon names instead of a positional path when it
withholds a fact.

**A card is an opaque leaf to the field-policy walk.** `garm.card.v1.Card`
joins the protobuf well-known types in `policy.IsOpaqueLeafMessage`: the field
that holds a card carries the policy for the whole value, and no scalar inside
one is classified separately. That is a policy decision, not an oversight — a
field policy is per descriptor and decides what *every* caller may see, while
a card's rows differ from one another, so the policy travels on the value. A
per-field policy inside a card would be a second mechanism answering the same
question differently. `CallRef` and `TaskRef` are ordinary request messages and
are classified field by field like anything else.

Two refs address the two things a card can be about: `CallRef{call_id}` is the
daemon's call id — the one id every tool sees and every ledger row carries —
and `TaskRef{task_id, material}` names an open task and carries the material
the client rebuilt from its card, because a tool parked at the approval gate
never saw the request that was parked. `Kind` gains `ASK`, for a question put
to a person that is not an approval.

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

- **C8** (error) — inside a template, a child may not be labelled below its
  `Section`. A `Section`'s label is a **floor**, not a value: the daemon
  removes what a viewer does not reach and it removes the section first, so a
  viewer who cannot read the heading never sees what was under it. A child
  labelled lower is therefore a fact labelled for an audience that can never
  reach it. An *absent* child label is fine — absent means "the enclosing
  policy", which is exactly the floor.
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

### Cards on every tool

Every tool in a catalogue carries up to three more methods that its author
never wrote:

```
<Method>InputCard    (google.protobuf.Empty) returns (garm.card.v1.Card)
<Method>ResultCard   (garm.card.v1.CallRef)  returns (garm.card.v1.Card)
<Method>ApprovalCard (garm.card.v1.TaskRef)  returns (garm.card.v1.Card)
```

A tool has a form, an answer and — if somebody has to approve it — an open
approval. Those are three more things a person needs to see about that tool,
and the only place they are governed the same way as the call itself is *as
calls to that tool*. So they are ordinary RPCs on the tool's own service, at
the tool's own clearance, through the same ten steps and into the same ledger.

**The author writes no proto.** `garm catalogue build` adds them to the
descriptor set, and `protoc-gen-garm-go` adds the Go for them, and both call
one function — `contracts/cards.Synthesise`. That sharing is the point and not
an optimisation: the catalogue is what the daemon routes by and the binding is
what the service registers, so if the two derived these names separately the
first mismatch would be a card the daemon dispatches and the service does not
serve, at run time, over a method neither author ever wrote. It is the same
argument `wire.Subject` makes one layer down.

**Naming.** A service with one tool, and an agent, get the bare names —
`InputCard`, `ResultCard` — because there is nothing to distinguish them from.
Everywhere else each card is prefixed with its parent's method name. The
catalogue *tool* name is always derived from the parent's, so
`initiate_payment` yields `initiate_payment_input_card` whichever spelling the
method took.

**The policy is the parent's**, with three things fixed and one added:

| | |
|---|---|
| copied | `min_clearance`, `compartments` — a card is exactly as visible as the tool it describes, which is the floor an element's own label may not go below |
| fixed | `verb: VERB_READ`; `approval: MODE_NONE`; `record_request` and `record_response` false — a card is built for one viewer at one moment and writing it down would put another viewer's withheld facts in the ledger. The audit *level* is still the parent's |
| added | `audience: [PERSON]`, whatever the parent's audience is. A card exists to be read by a person, and that is exactly why a model is never offered one |
| dropped | the parent's `sets`. A set says who holds a tool; holding `payments` is about calling payments tools, not about reading their forms |

Two methods get no cards. One whose response *is* a card — serving a form for
a form is a regress with nothing at the bottom. And an agent's `GetRun`, which
is the same run read a second way: only `Invoke` gets the pair. An agent's
`Invoke` is `MODE_NONE`, so it has no approval card either; an agent's
approvals are its tasks, and those cards come from the tasks tool.

**Synthesised methods are not in a package's `DescriptorHash`.** That digest
is what a tool service advertises and the daemon compares at mount, and a
service computes it from the tools its author declared. Hashing the cards
would move every existing service's digest without a wire shape having
changed.

**What the defaults produce.** `protoc-gen-garm-go` emits, beside the handler
interface, one default per card and a `<Service>Cards` interface documenting
what an override looks like:

| Card | Built from | Labels |
|---|---|---|
| input | the request descriptor — one control per field the caller may set, kind by type, `required`/`max_len`/`min`/`max` from protovalidate, caption from the leading comment. Fields the runner supplies are absent | each element at its field's **write** policy, joined with the endpoint's |
| result | `ResultCard(ctx, ref, resp)` — a fact per scalar of the response, in declaration order, `Fact.field` set | each fact at its field's **read** policy, joined with the endpoint's |
| approval | `Approval.material_fields`, valued from `TaskRef.material`, plus the owner and the declared `task_card` template | each fact at its field's read policy **joined with** the tool's `approver_min_clearance` and `approver_compartments` |

The card construction is not generated code: it lives in `contracts/cards` as
runtime functions over the method descriptor, and the generated default is a
three-line wrapper around one. Generating the construction would put hundreds
of lines of literal-building into every tool module and make a change to the
layout a regeneration of every repository that has one.

**The result card is the one that cannot be complete.** A card about an answer
needs the answer, and the wrapper has no way to reach a response the handler
returned to somebody else. The runtime is meant to seal each call's response
under its call id and hand it back; until that store exists the default
answers `result_unavailable`, and a tool that keeps its own record overrides
`ResultCard`, reads its own row and calls the generated
`<Method>ResultCardFrom` with it.

**Overriding.** Implement the method on your handler:

```go
func (p payments) InitiatePaymentApprovalCard(
    ctx context.Context, ref *cardv1.TaskRef,
) (*cardv1.Card, error) {
    // the material from the ref, your own data from your own store,
    // the invocation from ctx
}
```

`Serve<Service>` asserts each card's own signature, so overriding one leaves
every other card generated. An override **must label what it adds**: an
unlabelled element takes the endpoint's own policy, and an element labelled
*below* the endpoint fails the whole card rather than being served.
`cards.Join` is how to label something at the endpoint's floor or higher.

One lint rule guards the names, because they are added *after* lint runs:

- **C9** (error) — a hand-written method may not take a synthesised card's
  name, and a synthesised tool name must be a usable tool name (64 characters,
  and `_approval_card` is fourteen of them). A collision would put two methods
  of one name in the catalogue and leave which one a viewer reached to
  indexing order. The fix is usually not a proto at all: an override is a Go
  method on the generated `<Service>Cards` interface.

### Audience

A tool declares **who it is for**, in its own contract:

```proto
option (garm.tool.v1.tool) = {
  name: "decide_task" verb: VERB_WRITE min_clearance: CLEARANCE_PUBLIC
  audience: [AUDIENCE_PERSON]
};
```

`repeated Audience audience = 14` on `ToolPolicy`, with four values:
`AUDIENCE_AGENT` (a model may be offered it), `AUDIENCE_PERSON` (a person's
client may offer it), `AUDIENCE_RUNNER` (only the platform's runner) and
`AUDIENCE_UNSPECIFIED`, which is what an empty list means.

**Empty reads as `AGENT`.** Every tool declared before this field existed
keeps the audience it had, and person-facing is never something a tool falls
into by omission.

A tool set already says who *holds* a tool — `payments`, `support` — and an
earlier design tried to make sets carry this too, with a `studio` set for
cards and a `runner` set for the platform's own calls. It does not work: a
set answers "who has this", and the question a person's client has to answer
is "what is this for". `get_balance` and `screen_party` are tools a person's
claims may well reach, and neither should ever appear as a form: they are
what an agent calls, not what a person fills in. Sets go back to grouping by
domain; audience says what the tool is for.

The rule that follows is an AND, and neither half is new — a tool is offered
to a viewer when its audience admits that viewer's kind **and** the viewer's
folded claims reach it:

| Caller | Sees |
|---|---|
| a person's client | `PERSON` tools the person's claims reach |
| a run's model | `AGENT` tools the run's folded authority reaches, minus what the manifest's allowlist drops |
| the runner itself | `RUNNER` tools, called from the workflow and never from the loop |

Two rules run beside the agent rules:

- **A9** (error) — a manifest's allowlist may name only tools with
  `AUDIENCE_AGENT`. This is one rule where the set design needed three: a
  card endpoint, a person's decision and a runner's call are all the same
  fact. Catalogue-scoped like A3, so the protoc plugin warns rather than
  checking.
- **A10** (error) — a `MODE_GRANT` tool's approval card must admit a person.
  It is automatic, because a synthesised card carries `PERSON` whatever its
  parent is; it is checked because a hand-written method taking the card's
  name could declare otherwise, and a grant-mode tool whose approval card
  nobody can fetch is a queue nobody can clear.

The schema version does not move for this field. A daemon that predates it
ignores it and offers tools as it did before, which is the same trade
`source: SOURCE_RUNNER` took in v0.16.0: bumping would refuse every existing
catalogue at boot over an annotation the daemon is about to learn anyway.
`KNOWN-GAPS.md` says what that costs until it does.

### The tasks contract

`proto/garm/tasks/v1/tasks.proto` is the first SERVICE this repository
declares rather than a vocabulary. Nothing here serves it — `tasksd` does, in
its own repository — and garm publishes it for the reason it publishes the
annotations: the thing that serves it and the things that call it cannot drift
apart, and a catalogue carrying it is built by the same compiler as every
other tool.

Eight tools, each an ordinary governed call with a ledger row under its
caller:

| Tool | Audience | For |
|---|---|---|
| `create_task` | `RUNNER` | a runner opens an approval or a question for the run it is executing |
| `list_tasks` | `PERSON`, `AGENT` | the queue, as a list of cards |
| `get_task` | `PERSON`, `AGENT` | one task: its frame, its history, its card |
| `approval_card` | `PERSON` | the frame a person decides from |
| `claim_task` / `release_task` | `PERSON`, `AGENT` | take a task out of the shared queue, and put it back |
| `decide_task` | `PERSON` | approve, decline or answer |
| `triage_task` | `AGENT`, `PERSON` | recommend, comment, reassign or decline — never approve |

Three things about it are worth reading off the file:

**Every method is `CLEARANCE_PUBLIC` at the method gate, on purpose.** *Which*
task a viewer may see is decided per task by the label on its card, not per
method. A method-level gate would have to be the lowest predicate any tool in
the deployment declares, which is no gate at all.

**Field policies are flat and `PUBLIC`, with two exceptions.** The per-row
policy lives on the card; a field policy is per descriptor and cannot differ
per task. The two fields that do carry a static `RESTRICTED` read policy —
`Task.answer` and `DecideTaskRequest.grant` — are the ones whose value is data
for a machine rather than something a person reads.

**An agent can be on a queue without being able to say yes.** `triage_task` is
`AUDIENCE_AGENT`; `decide_task` is not, so A9 refuses a manifest that names
it, a model is never offered it, and the service refuses a caller whose chain
carries `act`. That last refusal is made in three places — the STS will not
mint from a delegated token, the service refuses one, and the daemon will not
verify one — so the rule survives any one of them being wrong.

`buf.yaml` excuses the file from four lint rules, and both excuses are design
decisions rather than conveniences. The enum spellings (`"state": "CLAIMED"`)
are the contract with a renderer, as `garm.card.v1`'s are. And
`garm.card.v1.TaskRef` is deliberately the request of four methods: a client
builds one ref for a task and uses it at every endpoint, including the target
tool's own approval card in another package, rather than coercing between four
identical per-RPC messages.

Importing `garm/tasks/v1/tasks.proto` from a tree is what puts these tools in
that tree's catalogue — the compiler collects a file's imports into the
descriptor set, and every annotated method in the set is a tool. The file is
linked into the CLI, so the import resolves without vendoring.

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
