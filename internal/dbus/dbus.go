// Package dbus is a minimal, hand-rolled D-Bus client: connect to a bus
// socket, SASL EXTERNAL authenticate, call a method, get the reply. Like
// internal/varlink, no existing Go D-Bus library is used here -- this
// project stays stdlib-only, and unlike Varlink's simple NUL-delimited
// JSON framing, D-Bus's binary wire protocol needs a real (if narrowly
// scoped) marshaler, implemented in marshal.go.
//
// This exists because two of the systemd-adjacent daemons slfhst talks
// to turned out to be D-Bus-only in practice, not Varlink, confirmed by
// inspecting the real running daemons rather than trusting docs:
// firewalld (org.fedoraproject.FirewallD1 -- this is actually
// firewall-cmd's own only interface, D-Bus has been the primary API here
// all along) and systemd-sysupdated (org.freedesktop.sysupdate1). Only
// the pieces of the D-Bus type system slfhst's actual calls need are
// implemented -- see marshal.go's doc comment for exactly what that
// covers.
package dbus

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

const systemBusAddress = "/run/dbus/system_bus_socket" // "/var/run/..." on some distros, same path via symlink.

// Conn is an authenticated connection to a D-Bus bus.
type Conn struct {
	c          net.Conn
	r          *bufio.Reader
	serial     uint32
	UniqueName string
}

// SystemBus connects to and authenticates against the system bus at its
// well-known socket path.
func SystemBus() (*Conn, error) {
	return Dial(systemBusAddress)
}

// Dial connects to a D-Bus daemon listening on the given Unix socket
// path and performs the SASL EXTERNAL handshake (authenticating as the
// calling process's UID, the standard mechanism for a local system-bus
// client) followed by the mandatory Hello() call.
func Dial(socketPath string) (*Conn, error) {
	raw, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dbus: dial %s: %w", socketPath, err)
	}
	conn := &Conn{c: raw, r: bufio.NewReader(raw)}

	if err := conn.authenticate(); err != nil {
		raw.Close()
		return nil, err
	}

	var name string
	if err := conn.Call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", "", nil, "s", []any{&name}); err != nil {
		raw.Close()
		return nil, fmt.Errorf("dbus: Hello: %w", err)
	}
	conn.UniqueName = name
	return conn, nil
}

func (c *Conn) authenticate() error {
	// The SASL exchange is a simple CRLF-terminated text protocol: a
	// leading NUL byte, then "AUTH EXTERNAL <hex-uid>", then "BEGIN"
	// once the server replies "OK". After BEGIN the connection switches
	// to the binary D-Bus message protocol used for everything else.
	if _, err := c.c.Write([]byte{0}); err != nil {
		return fmt.Errorf("dbus: auth: write NUL: %w", err)
	}
	uid := strconv.Itoa(os.Getuid())
	authLine := fmt.Sprintf("AUTH EXTERNAL %s\r\n", hex.EncodeToString([]byte(uid)))
	if _, err := c.c.Write([]byte(authLine)); err != nil {
		return fmt.Errorf("dbus: auth: write AUTH: %w", err)
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("dbus: auth: read reply: %w", err)
	}
	if len(line) < 2 || line[:2] != "OK" {
		return fmt.Errorf("dbus: auth: server rejected EXTERNAL auth: %q", line)
	}
	if _, err := c.c.Write([]byte("BEGIN\r\n")); err != nil {
		return fmt.Errorf("dbus: auth: write BEGIN: %w", err)
	}
	return nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) nextSerial() uint32 {
	return atomic.AddUint32(&c.serial, 1)
}

// Call invokes destination/path/iface.method with args marshaled per
// inSig, and unmarshals the reply body (per outSig) into dest -- pointers
// in the same order as outSig's top-level types. Pass "" and nil for a
// method with no input arguments, and "" and nil for one with no output.
//
// Signatures use the standard D-Bus type-signature grammar; see
// marshal.go for exactly which type codes are implemented.
func (c *Conn) Call(destination, path, iface, method, inSig string, args []any, outSig string, dest []any) error {
	serial := c.nextSerial()
	msg, err := encodeMethodCall(serial, destination, path, iface, method, inSig, args)
	if err != nil {
		return fmt.Errorf("dbus: encode %s.%s: %w", iface, method, err)
	}
	if _, err := c.c.Write(msg); err != nil {
		return fmt.Errorf("dbus: write %s.%s: %w", iface, method, err)
	}

	for {
		reply, err := readMessage(c.r)
		if err != nil {
			return fmt.Errorf("dbus: read reply to %s.%s: %w", iface, method, err)
		}
		if reply.replySerial != serial {
			// Not our reply (e.g. an unrelated signal) -- keep reading.
			continue
		}
		if reply.msgType == msgTypeError {
			return &CallError{Name: reply.errorName, Body: reply.body}
		}
		if outSig == "" {
			return nil
		}
		return decodeInto(reply.body, outSig, dest)
	}
}

// CallError is returned when the bus replies to a method call with an
// ERROR message.
type CallError struct {
	Name string
	Body []any
}

func (e *CallError) Error() string {
	return fmt.Sprintf("dbus: call error: %s %v", e.Name, e.Body)
}
