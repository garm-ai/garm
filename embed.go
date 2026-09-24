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

// AnnotationsPath is where AnnotationsProto belongs in a consumer's tree.
// The path is part of the contract: an import of "garm/tool/v1/tool.proto"
// has to resolve, so this is not a preference.
const AnnotationsPath = "proto/garm/tool/v1/tool.proto"
