package dbus

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	msgTypeMethodCall   = 1
	msgTypeMethodReturn = 2
	msgTypeError        = 3
	msgTypeSignal       = 4
)

const (
	fieldPath        = 1
	fieldInterface   = 2
	fieldMember      = 3
	fieldErrorName   = 4
	fieldReplySerial = 5
	fieldDestination = 6
	fieldSignature   = 8
)

// encodeMethodCall builds a complete METHOD_CALL message: the 12-byte
// fixed part (endianness/type/flags/protocol/body-length/serial), the
// header-fields array (reusing the generic a(yv) array/struct/variant
// encoding in marshal.go), padded to an 8-byte boundary, followed by the
// body.
func encodeMethodCall(serial uint32, destination, path, iface, method, inSig string, args []any) ([]byte, error) {
	body := &encoder{}
	if inSig != "" {
		rest := inSig
		i := 0
		for rest != "" {
			first, r, err := splitSig(rest)
			if err != nil {
				return nil, err
			}
			if i >= len(args) {
				return nil, fmt.Errorf("inSig %q needs %d args, got %d", inSig, len(rest), len(args))
			}
			if err := encodeValue(body, first, args[i]); err != nil {
				return nil, fmt.Errorf("arg %d: %w", i, err)
			}
			rest = r
			i++
		}
	}

	var fields []any
	addField := func(code byte, sig string, val any) {
		fields = append(fields, []any{code, Variant{Signature: sig, Value: val}})
	}
	addField(fieldPath, "o", path)
	addField(fieldInterface, "s", iface)
	addField(fieldMember, "s", method)
	if destination != "" {
		addField(fieldDestination, "s", destination)
	}
	if inSig != "" {
		addField(fieldSignature, "g", inSig)
	}

	header := &encoder{}
	header.buf = append(header.buf, 'l', byte(msgTypeMethodCall), 0, 1)
	header.putUint32(uint32(len(body.buf)))
	header.putUint32(serial)
	if err := encodeValue(header, "a(yv)", fields); err != nil {
		return nil, fmt.Errorf("header fields: %w", err)
	}
	for len(header.buf)%8 != 0 {
		header.buf = append(header.buf, 0)
	}

	msg := make([]byte, 0, len(header.buf)+len(body.buf))
	msg = append(msg, header.buf...)
	msg = append(msg, body.buf...)
	return msg, nil
}

// rawMessage is a parsed but not-yet-body-decoded incoming message.
type rawMessage struct {
	msgType     byte
	replySerial uint32
	errorName   string
	signature   string
	body        []any
}

// readMessage reads and fully decodes one message from r.
func readMessage(r *bufio.Reader) (*rawMessage, error) {
	fixed := make([]byte, 12)
	if _, err := io.ReadFull(r, fixed); err != nil {
		return nil, fmt.Errorf("read fixed header: %w", err)
	}
	if fixed[0] != 'l' {
		return nil, fmt.Errorf("unsupported byte order %q (only little-endian 'l' is implemented)", fixed[0])
	}
	msgType := fixed[1]
	bodyLen := binary.LittleEndian.Uint32(fixed[4:8])

	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, fmt.Errorf("read header-fields length: %w", err)
	}
	fieldsLen := binary.LittleEndian.Uint32(lenBuf)

	// Struct (array element) alignment: pad from the current absolute
	// offset (12 fixed + 4 length = 16) to the next multiple of 8.
	pos := 16
	padTo8 := (8 - pos%8) % 8
	if padTo8 > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(padTo8)); err != nil {
			return nil, fmt.Errorf("read header-fields padding: %w", err)
		}
		pos += padTo8
	}

	fieldsBuf := make([]byte, fieldsLen)
	if _, err := io.ReadFull(r, fieldsBuf); err != nil {
		return nil, fmt.Errorf("read header fields: %w", err)
	}
	pos += int(fieldsLen)

	headerPad := (8 - pos%8) % 8
	if headerPad > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(headerPad)); err != nil {
			return nil, fmt.Errorf("read header padding: %w", err)
		}
	}

	bodyBuf := make([]byte, bodyLen)
	if bodyLen > 0 {
		if _, err := io.ReadFull(r, bodyBuf); err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
	}

	fd := &decoder{buf: fieldsBuf}
	rawFields, err := decodeArrayElements(fd, "(yv)", len(fieldsBuf))
	if err != nil {
		return nil, fmt.Errorf("decode header fields: %w", err)
	}
	items, _ := rawFields.([]any)

	msg := &rawMessage{msgType: msgType}
	for _, item := range items {
		pair, ok := item.([]any)
		if !ok || len(pair) != 2 {
			continue
		}
		code, _ := pair[0].(byte)
		variant, _ := pair[1].(Variant)
		switch code {
		case fieldReplySerial:
			if u, ok := variant.Value.(uint32); ok {
				msg.replySerial = u
			}
		case fieldErrorName:
			if s, ok := variant.Value.(string); ok {
				msg.errorName = s
			}
		case fieldSignature:
			if s, ok := variant.Value.(string); ok {
				msg.signature = s
			}
		}
	}

	if msg.signature != "" && bodyLen > 0 {
		bd := &decoder{buf: bodyBuf}
		rest := msg.signature
		for rest != "" {
			first, r, err := splitSig(rest)
			if err != nil {
				return nil, fmt.Errorf("decode body: %w", err)
			}
			v, err := decodeValue(bd, first)
			if err != nil {
				return nil, fmt.Errorf("decode body value (%q): %w", first, err)
			}
			msg.body = append(msg.body, v)
			rest = r
		}
	}

	return msg, nil
}
