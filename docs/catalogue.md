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

It also refuses to write a catalogue with no tools in it. A daemon started on
an empty catalogue serves nothing, and that is never what anyone meant.

No `buf` required. No plugins, no registry, no network.

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
