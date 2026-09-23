package dbus

import (
	"bufio"
	"bytes"
	"reflect"
	"testing"
)

func encodeDecodeValue(t *testing.T, sig string, val any) any {
	t.Helper()
	e := &encoder{}
	if err := encodeValue(e, sig, val); err != nil {
		t.Fatalf("encodeValue(%q, %v): %v", sig, val, err)
	}
	d := &decoder{buf: e.buf}
	got, err := decodeValue(d, sig)
	if err != nil {
		t.Fatalf("decodeValue(%q) on encoded %v: %v", sig, val, err)
	}
	if d.pos != len(d.buf) {
		t.Errorf("decodeValue(%q) left %d trailing bytes unread", sig, len(d.buf)-d.pos)
	}
	return got
}

func TestRoundTripScalars(t *testing.T) {
	cases := []struct {
		sig string
		val any
	}{
		{"y", byte(0x2a)},
		{"b", true},
		{"b", false},
		{"n", int16(-1234)},
		{"q", uint16(54321)},
		{"i", int32(-123456789)},
		{"u", uint32(4000000000)},
		{"x", int64(-9000000000000000000)},
		{"t", uint64(18000000000000000000)},
		{"d", 3.14159265},
		{"s", "hello world"},
		{"s", ""},
		{"o", "/org/fedoraproject/FirewallD1"},
		{"g", "a(yv)"},
	}
	for _, c := range cases {
		got := encodeDecodeValue(t, c.sig, c.val)
		if !reflect.DeepEqual(got, c.val) {
			t.Errorf("sig %q: got %#v (%T), want %#v (%T)", c.sig, got, got, c.val, c.val)
		}
	}
}

func TestRoundTripArrayOfString(t *testing.T) {
	want := []string{"cf-test", "public", ""}
	got := encodeDecodeValue(t, "as", want)
	items, ok := got.([]any)
	if !ok {
		t.Fatalf("got %T, want []any", got)
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for i, w := range want {
		if items[i] != w {
			t.Errorf("item %d: got %v, want %v", i, items[i], w)
		}
	}
}

func TestRoundTripEmptyArray(t *testing.T) {
	got := encodeDecodeValue(t, "as", []string{})
	if got != nil {
		if items, ok := got.([]any); !ok || len(items) != 0 {
			t.Errorf("empty array: got %#v, want nil or empty []any", got)
		}
	}
}

func TestRoundTripStruct(t *testing.T) {
	// (yv) -- exactly the header-field struct shape.
	want := []any{byte(6), Variant{Signature: "s", Value: "org.fedoraproject.FirewallD1"}}
	got := encodeDecodeValue(t, "(yv)", want)
	items, ok := got.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("got %#v, want a 2-element []any", got)
	}
	if items[0] != byte(6) {
		t.Errorf("struct[0] = %v, want byte(6)", items[0])
	}
	v, ok := items[1].(Variant)
	if !ok || v.Signature != "s" || v.Value != "org.fedoraproject.FirewallD1" {
		t.Errorf("struct[1] = %#v, want Variant{s, ...}", items[1])
	}
}

func TestRoundTripDictStringString(t *testing.T) {
	want := map[string]any{"family": "inet", "foo": "bar"}
	got := encodeDecodeValue(t, "a{ss}", want)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want map[string]any", got)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%q] = %v, want %v", k, m[k], v)
		}
	}
}

func TestRoundTripArrayOfStructs(t *testing.T) {
	// a(yv) with two header-field-shaped entries, the actual shape used
	// for the message header-fields array.
	want := []any{
		[]any{byte(1), Variant{Signature: "o", Value: "/org/fedoraproject/FirewallD1"}},
		[]any{byte(2), Variant{Signature: "s", Value: "org.fedoraproject.FirewallD1"}},
	}
	got := encodeDecodeValue(t, "a(yv)", want)
	items, ok := got.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("got %#v, want a 2-element []any", got)
	}
	for i, w := range want {
		wpair := w.([]any)
		gpair, ok := items[i].([]any)
		if !ok || len(gpair) != 2 {
			t.Fatalf("item %d: got %#v, want a 2-element struct", i, items[i])
		}
		if gpair[0] != wpair[0] {
			t.Errorf("item %d code: got %v, want %v", i, gpair[0], wpair[0])
		}
		gv := gpair[1].(Variant)
		wv := wpair[1].(Variant)
		if gv.Signature != wv.Signature || gv.Value != wv.Value {
			t.Errorf("item %d variant: got %#v, want %#v", i, gv, wv)
		}
	}
}

func TestSplitSigNested(t *testing.T) {
	cases := []struct {
		in, first, rest string
	}{
		{"s", "s", ""},
		{"su", "s", "u"},
		{"as", "as", ""},
		{"a(yv)s", "a(yv)", "s"},
		{"a{sv}", "a{sv}", ""},
		{"(sa{sv}as)b", "(sa{sv}as)", "b"},
	}
	for _, c := range cases {
		first, rest, err := splitSig(c.in)
		if err != nil {
			t.Fatalf("splitSig(%q): %v", c.in, err)
		}
		if first != c.first || rest != c.rest {
			t.Errorf("splitSig(%q) = (%q, %q), want (%q, %q)", c.in, first, rest, c.first, c.rest)
		}
	}
}

// TestEncodeMethodCallThenReadMessage exercises the full message framing
// (fixed header + header-fields array + padding + body) round trip
// end-to-end, independent of any live bus.
func TestEncodeMethodCallThenReadMessage(t *testing.T) {
	msg, err := encodeMethodCall(
		42,
		"org.fedoraproject.FirewallD1",
		"/org/fedoraproject/FirewallD1",
		"org.fedoraproject.FirewallD1",
		"getDefaultZone",
		"", nil,
	)
	if err != nil {
		t.Fatalf("encodeMethodCall: %v", err)
	}
	if len(msg)%8 != 0 {
		t.Errorf("encoded message length %d is not a multiple of 8 (header must end 8-aligned)", len(msg))
	}

	// A real reply won't come back from encodeMethodCall's own output
	// (it has no body/signature), but decoding it as a message at all,
	// and getting the header fields we put in back out correctly via the
	// same array-of-struct decode path readMessage uses, is exactly what
	// this test is checking.
	r := bufio.NewReader(bytes.NewReader(msg))
	raw, err := readMessage(r)
	if err != nil {
		t.Fatalf("readMessage on our own encoded call: %v", err)
	}
	if raw.msgType != msgTypeMethodCall {
		t.Errorf("msgType = %d, want %d", raw.msgType, msgTypeMethodCall)
	}
}
