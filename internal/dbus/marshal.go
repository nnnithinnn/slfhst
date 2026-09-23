// Marshaling for the D-Bus binary wire protocol -- narrowly scoped to
// what slfhst's actual calls need, not a full binding library. Type
// codes implemented: y b n q i u x t d s o g a ( ) { } v -- i.e.
// everything except unix-fd (h), which none of firewalld's or
// sysupdate's interfaces slfhst calls use.
//
// The wire format's defining quirk is alignment: every type has a
// required byte alignment (1/2/4/8), and values are padded with zero
// bytes to reach it before being written, with padding computed
// relative to the start of whichever section is being encoded (the
// header fields array, or the body) treated as offset zero -- not the
// absolute position in the full message. See the D-Bus specification,
// "Marshaling (Wire Format)", for the authoritative rules this
// implements.
package dbus

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Variant wraps a value for the D-Bus "v" (variant) type, which carries
// its own inline type signature.
type Variant struct {
	Signature string
	Value     any
}

// ---- encoding ----

type encoder struct {
	buf []byte
}

func (e *encoder) align(n int) {
	for len(e.buf)%n != 0 {
		e.buf = append(e.buf, 0)
	}
}

func (e *encoder) putUint16(v uint16) {
	e.align(2)
	e.buf = binary.LittleEndian.AppendUint16(e.buf, v)
}
func (e *encoder) putUint32(v uint32) {
	e.align(4)
	e.buf = binary.LittleEndian.AppendUint32(e.buf, v)
}
func (e *encoder) putUint64(v uint64) {
	e.align(8)
	e.buf = binary.LittleEndian.AppendUint64(e.buf, v)
}

func (e *encoder) putStringLike(s string) { // 's' or 'o': uint32 length + bytes + NUL
	e.putUint32(uint32(len(s)))
	e.buf = append(e.buf, s...)
	e.buf = append(e.buf, 0)
}

func (e *encoder) putSignature(s string) { // 'g': byte length + bytes + NUL
	e.buf = append(e.buf, byte(len(s)))
	e.buf = append(e.buf, s...)
	e.buf = append(e.buf, 0)
}

// alignmentOf returns the required byte alignment for the type whose
// signature starts at sig[0].
func alignmentOf(sig string) int {
	switch sig[0] {
	case 'y', 'g':
		return 1
	case 'n', 'q':
		return 2
	case 'b', 'i', 'u', 's', 'o', 'a':
		return 4
	case 'x', 't', 'd', '(', '{', 'v':
		return 8
	default:
		return 1
	}
}

// splitSig returns the first complete type in sig and the remainder,
// handling nested STRUCT (...) and DICT_ENTRY {...} correctly.
func splitSig(sig string) (first, rest string, err error) {
	if sig == "" {
		return "", "", fmt.Errorf("dbus: empty signature")
	}
	switch sig[0] {
	case 'a':
		inner, r, err := splitSig(sig[1:])
		if err != nil {
			return "", "", err
		}
		return "a" + inner, r, nil
	case '(':
		depth := 1
		i := 1
		for ; i < len(sig) && depth > 0; i++ {
			switch sig[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			return "", "", fmt.Errorf("dbus: unbalanced () in signature %q", sig)
		}
		return sig[:i], sig[i:], nil
	case '{':
		depth := 1
		i := 1
		for ; i < len(sig) && depth > 0; i++ {
			switch sig[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth != 0 {
			return "", "", fmt.Errorf("dbus: unbalanced {} in signature %q", sig)
		}
		return sig[:i], sig[i:], nil
	default:
		return sig[:1], sig[1:], nil
	}
}

func encodeValue(e *encoder, sig string, val any) error {
	switch sig[0] {
	case 'y':
		v, ok := val.(byte)
		if !ok {
			return fmt.Errorf("dbus: 'y' wants byte, got %T", val)
		}
		e.buf = append(e.buf, v)
	case 'b':
		v, ok := val.(bool)
		if !ok {
			return fmt.Errorf("dbus: 'b' wants bool, got %T", val)
		}
		if v {
			e.putUint32(1)
		} else {
			e.putUint32(0)
		}
	case 'n':
		v, ok := val.(int16)
		if !ok {
			return fmt.Errorf("dbus: 'n' wants int16, got %T", val)
		}
		e.putUint16(uint16(v))
	case 'q':
		v, ok := val.(uint16)
		if !ok {
			return fmt.Errorf("dbus: 'q' wants uint16, got %T", val)
		}
		e.putUint16(v)
	case 'i':
		v, ok := val.(int32)
		if !ok {
			return fmt.Errorf("dbus: 'i' wants int32, got %T", val)
		}
		e.putUint32(uint32(v))
	case 'u':
		v, ok := val.(uint32)
		if !ok {
			return fmt.Errorf("dbus: 'u' wants uint32, got %T", val)
		}
		e.putUint32(v)
	case 'x':
		v, ok := val.(int64)
		if !ok {
			return fmt.Errorf("dbus: 'x' wants int64, got %T", val)
		}
		e.putUint64(uint64(v))
	case 't':
		v, ok := val.(uint64)
		if !ok {
			return fmt.Errorf("dbus: 't' wants uint64, got %T", val)
		}
		e.putUint64(v)
	case 'd':
		v, ok := val.(float64)
		if !ok {
			return fmt.Errorf("dbus: 'd' wants float64, got %T", val)
		}
		e.putUint64(math.Float64bits(v))
	case 's', 'o':
		v, ok := val.(string)
		if !ok {
			return fmt.Errorf("dbus: '%c' wants string, got %T", sig[0], val)
		}
		e.putStringLike(v)
	case 'g':
		v, ok := val.(string)
		if !ok {
			return fmt.Errorf("dbus: 'g' wants string, got %T", val)
		}
		e.putSignature(v)
	case 'v':
		vv, ok := val.(Variant)
		if !ok {
			return fmt.Errorf("dbus: 'v' wants dbus.Variant, got %T", val)
		}
		e.putSignature(vv.Signature)
		if err := encodeValue(e, vv.Signature, vv.Value); err != nil {
			return fmt.Errorf("dbus: variant body: %w", err)
		}
	case 'a':
		return encodeArray(e, sig, val)
	case '(':
		items, ok := val.([]any)
		if !ok {
			return fmt.Errorf("dbus: '%s' wants []any, got %T", sig, val)
		}
		e.align(8)
		members := sig[1 : len(sig)-1]
		rest := members
		i := 0
		for rest != "" {
			var first string
			var err error
			first, rest, err = splitSig(rest)
			if err != nil {
				return err
			}
			if i >= len(items) {
				return fmt.Errorf("dbus: struct %q needs %d members, got %d", sig, len(members), len(items))
			}
			if err := encodeValue(e, first, items[i]); err != nil {
				return fmt.Errorf("dbus: struct member %d: %w", i, err)
			}
			i++
		}
	default:
		return fmt.Errorf("dbus: unsupported type code %q in signature %q", sig[0], sig)
	}
	return nil
}

func encodeArray(e *encoder, sig string, val any) error {
	elemSig := sig[1:]

	e.putUint32(0) // length placeholder, patched below
	lenPos := len(e.buf) - 4
	e.align(alignmentOf(elemSig))
	dataStart := len(e.buf)

	if elemSig[0] == '{' {
		m, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("dbus: %q wants map[string]any, got %T", sig, val)
		}
		keySig, valSig, _, err := splitDictEntry(elemSig)
		if err != nil {
			return err
		}
		for k, v := range m {
			e.align(8)
			if err := encodeValue(e, keySig, k); err != nil {
				return fmt.Errorf("dbus: dict key: %w", err)
			}
			if err := encodeValue(e, valSig, v); err != nil {
				return fmt.Errorf("dbus: dict value for %q: %w", k, err)
			}
		}
	} else {
		items, err := toAnySlice(val)
		if err != nil {
			return fmt.Errorf("dbus: array %q: %w", sig, err)
		}
		for i, item := range items {
			if err := encodeValue(e, elemSig, item); err != nil {
				return fmt.Errorf("dbus: array element %d: %w", i, err)
			}
		}
	}

	length := len(e.buf) - dataStart
	binary.LittleEndian.PutUint32(e.buf[lenPos:lenPos+4], uint32(length))
	return nil
}

// toAnySlice accepts the handful of slice shapes call sites actually
// pass (kept deliberately small rather than fully generic via
// reflection, matching the rest of this package's narrow scope).
func toAnySlice(val any) ([]any, error) {
	switch v := val.(type) {
	case []any:
		return v, nil
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out, nil
	case []byte:
		out := make([]any, len(v))
		for i, b := range v {
			out[i] = b
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported array value type %T", val)
	}
}

func splitDictEntry(sig string) (keySig, valSig, rest string, err error) {
	if len(sig) < 2 || sig[0] != '{' {
		return "", "", "", fmt.Errorf("dbus: %q is not a dict entry", sig)
	}
	inner := sig[1 : len(sig)-1]
	keySig, rest, err = splitSig(inner)
	if err != nil {
		return "", "", "", err
	}
	valSig, rest, err = splitSig(rest)
	if err != nil {
		return "", "", "", err
	}
	return keySig, valSig, rest, nil
}

// ---- decoding ----

type decoder struct {
	buf []byte
	pos int
}

func (d *decoder) align(n int) error {
	for d.pos%n != 0 {
		if d.pos >= len(d.buf) {
			return fmt.Errorf("dbus: unexpected end of message while aligning")
		}
		d.pos++
	}
	return nil
}

func (d *decoder) need(n int) error {
	if d.pos+n > len(d.buf) {
		return fmt.Errorf("dbus: unexpected end of message (need %d bytes at %d, have %d)", n, d.pos, len(d.buf))
	}
	return nil
}

func (d *decoder) getByte() (byte, error) {
	if err := d.need(1); err != nil {
		return 0, err
	}
	b := d.buf[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) getUint16() (uint16, error) {
	if err := d.align(2); err != nil {
		return 0, err
	}
	if err := d.need(2); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint16(d.buf[d.pos:])
	d.pos += 2
	return v, nil
}

func (d *decoder) getUint32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	if err := d.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(d.buf[d.pos:])
	d.pos += 4
	return v, nil
}

func (d *decoder) getUint64() (uint64, error) {
	if err := d.align(8); err != nil {
		return 0, err
	}
	if err := d.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(d.buf[d.pos:])
	d.pos += 8
	return v, nil
}

func (d *decoder) getStringLike() (string, error) {
	n, err := d.getUint32()
	if err != nil {
		return "", err
	}
	if err := d.need(int(n) + 1); err != nil {
		return "", err
	}
	s := string(d.buf[d.pos : d.pos+int(n)])
	d.pos += int(n) + 1 // + trailing NUL
	return s, nil
}

func (d *decoder) getSignature() (string, error) {
	n, err := d.getByte()
	if err != nil {
		return "", err
	}
	if err := d.need(int(n) + 1); err != nil {
		return "", err
	}
	s := string(d.buf[d.pos : d.pos+int(n)])
	d.pos += int(n) + 1
	return s, nil
}

func decodeValue(d *decoder, sig string) (any, error) {
	switch sig[0] {
	case 'y':
		return d.getByte()
	case 'b':
		v, err := d.getUint32()
		return v != 0, err
	case 'n':
		v, err := d.getUint16()
		return int16(v), err
	case 'q':
		return d.getUint16()
	case 'i':
		v, err := d.getUint32()
		return int32(v), err
	case 'u':
		return d.getUint32()
	case 'x':
		v, err := d.getUint64()
		return int64(v), err
	case 't':
		return d.getUint64()
	case 'd':
		v, err := d.getUint64()
		return math.Float64frombits(v), err
	case 's', 'o':
		return d.getStringLike()
	case 'g':
		return d.getSignature()
	case 'v':
		vsig, err := d.getSignature()
		if err != nil {
			return nil, err
		}
		val, err := decodeValue(d, vsig)
		if err != nil {
			return nil, err
		}
		return Variant{Signature: vsig, Value: val}, nil
	case 'a':
		return decodeArray(d, sig)
	case '(':
		if err := d.align(8); err != nil {
			return nil, err
		}
		members := sig[1 : len(sig)-1]
		rest := members
		var out []any
		for rest != "" {
			first, r, err := splitSig(rest)
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(d, first)
			if err != nil {
				return nil, fmt.Errorf("struct member: %w", err)
			}
			out = append(out, v)
			rest = r
		}
		return out, nil
	default:
		return nil, fmt.Errorf("dbus: unsupported type code %q in signature %q", sig[0], sig)
	}
}

func decodeArray(d *decoder, sig string) (any, error) {
	elemSig := sig[1:]
	length, err := d.getUint32()
	if err != nil {
		return nil, err
	}
	if err := d.align(alignmentOf(elemSig)); err != nil {
		return nil, err
	}
	end := d.pos + int(length)
	if err := d.need(int(length)); err != nil {
		return nil, err
	}
	return decodeArrayElements(d, elemSig, end)
}

// decodeArrayElements consumes array elements of type elemSig from d
// until d.pos reaches end. Split out of decodeArray so callers that
// already know an array's data boundary from elsewhere on the wire (the
// message header-fields array, whose length was read directly off the
// stream by readMessage rather than out of an in-memory buffer) can
// decode the same element shapes without duplicating this logic.
func decodeArrayElements(d *decoder, elemSig string, end int) (any, error) {
	if elemSig[0] == '{' {
		keySig, valSig, _, err := splitDictEntry(elemSig)
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		for d.pos < end {
			if err := d.align(8); err != nil {
				return nil, err
			}
			kv, err := decodeValue(d, keySig)
			if err != nil {
				return nil, fmt.Errorf("dict key: %w", err)
			}
			k, ok := kv.(string)
			if !ok {
				return nil, fmt.Errorf("dbus: only string-keyed dicts are supported, got key type %T", kv)
			}
			v, err := decodeValue(d, valSig)
			if err != nil {
				return nil, fmt.Errorf("dict value for %q: %w", k, err)
			}
			out[k] = v
		}
		return out, nil
	}

	var out []any
	for d.pos < end {
		v, err := decodeValue(d, elemSig)
		if err != nil {
			return nil, fmt.Errorf("array element: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
}

// decodeInto walks outSig's top-level types against body (already
// decoded into []any by the message reader) and assigns each into the
// matching pointer in dest, in order.
func decodeInto(body []any, outSig string, dest []any) error {
	rest := outSig
	i := 0
	for rest != "" {
		first, r, err := splitSig(rest)
		if err != nil {
			return err
		}
		if i >= len(body) || i >= len(dest) {
			return fmt.Errorf("dbus: outSig %q expects more values than reply/dest provided", outSig)
		}
		if err := assign(dest[i], body[i], first); err != nil {
			return fmt.Errorf("dbus: out value %d (%q): %w", i, first, err)
		}
		rest = r
		i++
	}
	return nil
}

func assign(dest any, val any, sig string) error {
	switch d := dest.(type) {
	case *string:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("want string, got %T", val)
		}
		*d = s
	case *bool:
		b, ok := val.(bool)
		if !ok {
			return fmt.Errorf("want bool, got %T", val)
		}
		*d = b
	case *uint32:
		u, ok := val.(uint32)
		if !ok {
			return fmt.Errorf("want uint32, got %T", val)
		}
		*d = u
	case *uint64:
		u, ok := val.(uint64)
		if !ok {
			return fmt.Errorf("want uint64, got %T", val)
		}
		*d = u
	case *[]string:
		items, ok := val.([]any)
		if !ok {
			return fmt.Errorf("want array, got %T", val)
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("array element: want string, got %T", item)
			}
			out = append(out, s)
		}
		*d = out
	case *[]any:
		items, _ := val.([]any)
		*d = items
	case *map[string]any:
		m, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("want dict, got %T", val)
		}
		*d = m
	case *any:
		*d = val
	default:
		return fmt.Errorf("unsupported dest type %T for signature %q", dest, sig)
	}
	return nil
}
