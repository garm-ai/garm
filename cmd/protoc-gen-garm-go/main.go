// Command protoc-gen-garm-go is the garm tool generator for Go, in the shape
// protoc and buf expect to find it: a standalone binary named
// protoc-gen-<name>, discoverable on PATH.
//
// It is a shim. The generator lives in internal/plugin, and the garm CLI
// exposes the same entry point as `garm protoc-gen-go`. Same module, same
// tag, so the annotations and the generator that reads them cannot skew —
// which is the reason for bundling, kept without giving up the conventional
// name people expect to install.
package main

import "github.com/garm-ai/garm/internal/plugin"

func main() { plugin.Run() }
