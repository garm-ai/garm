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

## What it costs, and what pays for it

The thing given up is real: a self-contained binary used to be able to say
"these are my tools" from its own build. That property is load-bearing for
governance, not decoration, so it is replaced rather than dropped.

**Identity becomes the pair `(binary version, catalogue digest)`.**

Both appear in the startup line, on the health endpoint, in `GetCatalogue`,
and on every ledger event. *Which tools is this process serving* stays exactly
answerable, and provable from an audit record months later.

This is the shape Envoy and OPA use: a slow, stable binary plus a fast,
versioned, digest-identified policy artifact.

## What is in one

| | |
|---|---|
| `annotation_schema_version` | Which vocabulary the declarations speak |
| `files` | A `FileDescriptorSet` — every proto needed, including transitive imports |
| `compartments`, `tool_sets` | The taxonomy the declarations refer to, aggregated across packages at build time |
| `provenance` | Producer, compiler, optional source and build time |

**Self-contained on purpose.** A daemon resolves nothing at boot and reaches no
registry. Whatever the catalogue does not carry, it does not have.

**The taxonomy is aggregated at build time, not derived at load.** The build is
where two packages declaring the same compartment two ways can be caught. A
daemon that had to reconcile them would be deciding policy, which is not its
job.

**Provenance is for humans.** It is not trusted for anything. It exists for the
operator reading a startup line and the engineer reading an incident.

## The digest

Computed with SHA-256 **over the artifact bytes, before anything is parsed.**

Digesting after parsing would record what the daemon understood rather than
what it was handed — which is the wrong thing to identify, and which would
quietly change meaning the day a parser changed.

### Reproducibility is a property, not a nicety

**Two builds of identical source produce identical bytes.** If they did not,
the digest would identify the *build* rather than the content — a redeploy of
unchanged source would look like a change, and "are these two deployments
serving the same tools?" would be unanswerable.

That is why the build time is **off by default**. `--stamp-time` records it and
says in its own help what it costs. A wall clock buys little that `--source`
(a repository and commit) does not already carry.

It is also why compilation is in process, against a compiler this binary pins,
rather than shelling out to `buf`: a digest that moves when a contributor
upgrades a CLI on their laptop identifies nothing. The compiler version is
recorded in provenance, so a digest mismatch is a diff rather than a mystery.

## What it costs

Measured on synthetic catalogues, one process, descriptors retained after the
raw bytes and the `FileDescriptorSet` are dropped:

| tools | artifact | load | retained heap |
|---|---|---|---|
| 100 | 40 KB | 3 ms | 1 MB |
| 1,000 | 400 KB | 17 ms | 10 MB |
| 10,000 | 3.8 MB | 170 ms | 93 MB |

Linear throughout. The load time is almost entirely protobuf building and
cross-resolving the registry; reading the annotations off it is under 2 ms at
10,000 tools.

### Comments are stripped, prose is not

`SourceCodeInfo` carries spans and paths for every token in every file, and it
is roughly **half** of both numbers above — before the strip, 10,000 tools cost
9.7 MB on disk and 180 MB retained.

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
