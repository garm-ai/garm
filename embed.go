// Package garm embeds the contract this repository publishes.
//
// The annotations are vendored into a consumer's tree by `garm init` rather
// than fetched from a registry. That is deliberate: a registry is one more
// thing an enterprise build has to be allowed to reach, and the annotations
// are small, stable, and worth reading by the people writing tool schemas
// against them.
//
// The bytes here are the same bytes `garm init` writes, verbatim and with no
// added header, so a later drift check is a byte comparison rather than a
// parse.
package garm

import _ "embed"

// AnnotationsProto is proto/garm/tool/v1/tool.proto, exactly as published.
//
//go:embed proto/garm/tool/v1/tool.proto
var AnnotationsProto []byte

// VendoredAnnotationsPath is where AnnotationsProto belongs in a consumer's
// tree.
//
// Under third_party rather than beside your own protos, and that is not
// cosmetic: every module in a buf v2 workspace is an input, so annotations
// living under proto/ get Go generated for them — output that is never
// usable, because the real one is in this module's contracts package and two
// packages registering one proto file panic at init.
//
// The tail of the path is part of the contract: an import of
// "garm/tool/v1/tool.proto" has to resolve, so only the root is a choice.
const VendoredAnnotationsPath = "third_party/proto/garm/tool/v1/tool.proto"

// AnnotationSchemaVersion is the version of the garm.tool.v1 vocabulary this
// build speaks, stamped into every catalogue it produces.
//
// A daemon reads the current version and the two previous and refuses anything
// outside that window at boot. It is an integer rather than a semver because
// the only question asked of it is "can this binary read that catalogue", and
// a range check wants a number.
//
// Bump it when a change to the annotations means an older daemon would
// misread a declaration — not when a field is added that an older daemon can
// safely ignore.
const AnnotationSchemaVersion = 1
