// Package toolgen reads garm tool annotations off descriptors and validates them.
package compiler

import (
	"strings"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
)

// Tool is one RPC that is exposed as a tool.
type Tool struct {
	Method protoreflect.MethodDescriptor
	Policy *toolv1.ToolPolicy
	Name   string // resolved: explicit name, or SnakeCase(method)
}

// Tools collects every non-excluded annotated method.
func Tools(fds []protoreflect.FileDescriptor) ([]Tool, error) {
	var out []Tool
	for _, fd := range fds {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				md := svc.Methods().Get(j)
				tp := methodPolicy(md)
				if tp == nil || tp.GetExclude() {
					continue
				}
				name := tp.GetName()
				if name == "" {
					name = DefaultToolName(md)
				}
				out = append(out, Tool{Method: md, Policy: tp, Name: name})
			}
		}
	}
	return out, nil
}

func methodPolicy(md protoreflect.MethodDescriptor) *toolv1.ToolPolicy {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(opts, toolv1.E_Tool) {
		return nil
	}
	tp, _ := proto.GetExtension(opts, toolv1.E_Tool).(*toolv1.ToolPolicy)
	return tp
}

// DeclaredCompartments unions the file-level declarations across the input set.
func DeclaredCompartments(fds []protoreflect.FileDescriptor) []*toolv1.Decl {
	return declared(fds, toolv1.E_Compartments)
}

// DeclaredSets unions the file-level tool-set declarations.
func DeclaredSets(fds []protoreflect.FileDescriptor) []*toolv1.Decl {
	return declared(fds, toolv1.E_ToolSets)
}

func declared(fds []protoreflect.FileDescriptor, xt protoreflect.ExtensionType) []*toolv1.Decl {
	seen := map[string]bool{}
	var out []*toolv1.Decl
	for _, fd := range fds {
		opts, ok := fd.Options().(*descriptorpb.FileOptions)
		if !ok || !proto.HasExtension(opts, xt) {
			continue
		}
		ds, _ := proto.GetExtension(opts, xt).(*toolv1.DeclSet)
		for _, d := range ds.GetDeclared() {
			if seen[d.GetName()] {
				continue
			}
			seen[d.GetName()] = true
			out = append(out, d)
		}
	}
	return out
}

// SnakeCase converts an RPC name to the default MCP tool name.
//
// Runs of capitals are treated as one word, so GetHTTPStatus becomes
// get_http_status rather than get_h_t_t_p_status.
func SnakeCase(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(rs[i-1])
			nextLower := i+1 < len(rs) && !unicode.IsUpper(rs[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DefaultToolName returns the default tool name derived from a method
// descriptor's name, without consulting any explicit name in policy.
func DefaultToolName(md protoreflect.MethodDescriptor) string {
	return SnakeCase(string(md.Name()))
}
