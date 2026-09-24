# Conformance

What garm's tool rules accept and refuse, as files you can read.

Each case is one `.proto` and the exact diagnostics garm produces for it. The
audience is someone writing a tool schema, not someone maintaining the linter:
*why was this rejected* and *what does an acceptable declaration look like* are
answered by the same directory.

```
cases/
  valid-calculator/                 what acceptable looks like — expected.txt is empty
  L1-field-without-policy/          every field needs a policy or a message default
  L5-mask-on-non-string/            mask is for strings; a number masked is a number changed
  L6-omit-without-presence/         omit needs `optional`, or redacted and empty look the same
  L14-L21-destructive-unsupervised/ a destructive tool must declare approval and audit
```

## Running

```console
$ go test ./conformance/
```

After a deliberate rule change:

```console
$ go test ./conformance/ -update
```

then **read the diff**. These messages are the contract with everyone writing
schemas — a changed message is a changed contract, and should be reviewed as
one rather than regenerated past.

## Coverage

Five cases against 27 rules. This is a reference, not an exhaustive matrix:
the rules themselves are covered by unit tests in `internal/compiler`, which is
the right place for a rule's edges. What belongs here is the handful a schema
author actually trips over, written so the message and the `.proto` that
caused it sit side by side.

Add a case when a rule turns out to surprise someone. That is better evidence
than a coverage count.

## Why L6 is the interesting one

L5 is a type rule: mask is a string operation, so masking a `double` is
incoherent.

L6 is not. It refuses `omit` on a proto3 scalar without presence, because the
wire result of omitting one is indistinguishable from it being genuinely zero.
The caller cannot tell *you may not see this* from *it is 0*. That is a
redaction which leaks by ambiguity, and it is exactly the kind of thing that
looks fine in review and is wrong in production — which is why it is a build
error rather than a warning.
