package policy_test

import (
	"fmt"
	"testing"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/policy"
	"github.com/garm-ai/garm/policy/redact"
	"github.com/garm-ai/garm/policy/testdata"
	"github.com/garm-ai/garm/policy/testdata/testdatagarm"
	"google.golang.org/protobuf/proto"
)

// Where a governed call spends its time, on the two steps that walk a message.
//
// Compile happens once per message type and is cached. Resolve happens once
// per (plan, shape) and is cached. Sanitize happens on EVERY call, so it is
// the one whose cost multiplies by traffic.
//
//	go test ./policy/ -bench=. -benchtime=2s -run=XXX

// sample is a profile with every governed field populated: a plain string, a
// presence-having string, a compartmented one, a nested message and a map.
// Sanitising it walks all of them, which is the point.
func sample() *testdata.Profile {
	return &testdata.Profile{
		Id:         "u_8123",
		Locale:     "en-GB",
		Email:      proto.String("ada@corp.com"),
		NationalId: proto.String("NI-99-88-77"),
		Billing:    &testdata.Billing{CardLast4: "1111", Iban: "GB33BUKB20201555555555"},
		Tags:       map[string]string{"tier": "gold"},
	}
}

func reg(b *testing.B) *policy.Registry {
	b.Helper()
	r, err := policy.NewRegistry(testdatagarm.Compartments)
	if err != nil {
		b.Fatal(err)
	}
	return r
}

// BenchmarkCompile is the cost paid once per message type, at mount. It does
// not multiply by traffic, which is why it is allowed to be the expensive one.
func BenchmarkCompile(b *testing.B) {
	r := reg(b)
	md := (&testdata.Profile{}).ProtoReflect().Descriptor()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := policy.Compile(md, r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResolve is paid once per (plan, shape) and cached by shape, so it
// multiplies by the number of DISTINCT principals rather than by calls.
func BenchmarkResolve(b *testing.B) {
	r := reg(b)
	plan, err := policy.Compile((&testdata.Profile{}).ProtoReflect().Descriptor(), r)
	if err != nil {
		b.Fatal(err)
	}
	shape := policy.Shape{Clearance: toolv1.Clearance_CLEARANCE_INTERNAL}
	cache := policy.NewCache()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.Resolve(plan, shape)
	}
}

// BenchmarkSanitize is the one that matters: paid on every response, on the
// critical path, and linear in what it walks.
func BenchmarkSanitize(b *testing.B) {
	r := reg(b)
	plan, err := policy.Compile((&testdata.Profile{}).ProtoReflect().Descriptor(), r)
	if err != nil {
		b.Fatal(err)
	}
	resolved := policy.NewCache().Resolve(plan, policy.Shape{
		Clearance: toolv1.Clearance_CLEARANCE_INTERNAL,
	})

	for _, n := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("%dmessages", n), func(b *testing.B) {
			msgs := make([]*testdata.Profile, n)
			for i := range msgs {
				msgs[i] = sample()
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, m := range msgs {
					_ = policy.Sanitize(m, resolved, redact.Ctx{})
				}
			}
		})
	}
}
