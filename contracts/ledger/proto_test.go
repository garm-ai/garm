package ledger_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/garm-ai/garm/contracts/ledger"
)

// Every field of Event must survive the wire.
//
// This is reflective rather than a hand-written literal on purpose. A field
// added to Event and forgotten in ToProto reaches the recorder and never
// reaches the lake, and NOTHING fails: the publish succeeds, the row is
// written, the column is empty. Someone notices months later when a query
// returns blanks, if at all.
//
// So the test fills every field with a distinct non-zero value by reflection
// and asserts the round trip is identity. A new field is non-zero going in
// and zero coming back until the mapping learns it, and this fails by name.
func TestEveryEventFieldSurvivesTheRoundTrip(t *testing.T) {
	var ev ledger.Event
	fill(t, reflect.ValueOf(&ev).Elem())

	got := ledger.FromProto(ledger.ToProto(ev))

	rv, rg := reflect.ValueOf(ev), reflect.ValueOf(got)
	for i := 0; i < rv.NumField(); i++ {
		name := rv.Type().Field(i).Name
		if !reflect.DeepEqual(rv.Field(i).Interface(), rg.Field(i).Interface()) {
			t.Errorf("Event.%s did not survive: sent %v, got back %v. Add %s to "+
				"ToProto and FromProto — until then it is recorded and never lands "+
				"in the lake, and nothing reports it",
				name, rv.Field(i).Interface(), rg.Field(i).Interface(), name)
		}
	}
}

// fill writes a distinct non-zero value into every field, and FAILS on a type
// it has no value for rather than skipping it. A silent skip would let a new
// field of a new type pass this test while being dropped on the wire, which
// is the exact failure the test exists to catch.
func fill(t *testing.T, v reflect.Value) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.String:
			f.SetString("x-" + name)
		case reflect.Int, reflect.Int32, reflect.Int64:
			if f.Type() == reflect.TypeOf(time.Duration(0)) {
				f.SetInt(int64(time.Second))
				continue
			}
			f.SetInt(int64(i + 1))
		case reflect.Float64:
			f.SetFloat(float64(i) + 0.5)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				t.Fatalf("Event.%s is a slice of %s, which this test has no value for; "+
					"add one and check ToProto maps it", name, f.Type().Elem())
			}
			f.Set(reflect.ValueOf([]string{"a-" + name, "b-" + name}))
		case reflect.Map:
			f.Set(reflect.ValueOf(map[string]string{"k": "v-" + name}))
		case reflect.Struct:
			if f.Type() == reflect.TypeOf(time.Time{}) {
				// Truncated to microseconds: protobuf Timestamp carries
				// nanoseconds but a round trip through it is only exact to
				// what the encoder preserves, and a test failing on clock
				// precision teaches nothing.
				f.Set(reflect.ValueOf(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))
				continue
			}
			fill(t, f)
		default:
			t.Fatalf("Event.%s has kind %s, which this test has no value for; add one "+
				"and check ToProto maps it", name, f.Kind())
		}
	}
}

// A nil event must not panic. A forwarder decoding a malformed message gets
// nil here, and a panic in a ledger consumer takes down the thing that was
// meant to be the durable one.
func TestFromProtoToleratesNil(t *testing.T) {
	if got := ledger.FromProto(nil); got.ID != "" {
		t.Errorf("FromProto(nil) = %+v, want the zero Event", got)
	}
}

// Two ids are not the same id. The dedupe the lake performs is only as good
// as this.
func TestNewEventIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := ledger.NewEventID()
		if id == "" {
			t.Fatal("NewEventID returned empty; every event must be dedupable")
		}
		if seen[id] {
			t.Fatalf("NewEventID repeated %q after %d draws", id, i)
		}
		seen[id] = true
	}
}
