package compiler

import "testing"

func TestExportIdent(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"financial", "Financial"},
		{"pii-contact", "PiiContact"},
		{"a_b_c", "ABC"},
		{"", ""},
	} {
		if got := exportIdent(c.in); got != c.want {
			t.Fatalf("exportIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGoStringSlice(t *testing.T) {
	if got := goStringSlice(nil); got != "nil" {
		t.Fatalf("goStringSlice(nil) = %q, want %q", got, "nil")
	}
	if got := goStringSlice([]string{}); got != "nil" {
		t.Fatalf("goStringSlice([]string{}) = %q, want %q", got, "nil")
	}
	if got := goStringSlice([]string{"a", "b"}); got != `[]string{"a", "b"}` {
		t.Fatalf("goStringSlice = %q", got)
	}
}
