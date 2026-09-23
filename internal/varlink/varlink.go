// Package varlink is a minimal, hand-rolled Varlink client. No existing
// Go client library was found for this (confirmed via DeepWiki against
// systemd/systemd), and the wire protocol is simple and stable enough to
// implement directly against net + encoding/json rather than pull in a
// third-party dependency, matching this project's stdlib-only
// convention: connect to a Unix socket, exchange JSON objects each
// terminated by a single NUL byte. See https://varlink.org/Wire-Format.
//
// This package only implements what slfhst needs: a single call, a
// single reply. It does not (yet) support "more" (streaming multi-reply)
// calls -- add that if/when a Phase 0 spike against a real interface
// (e.g. io.systemd.SysUpdate) turns out to need it.
package varlink

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Conn is a connection to a single Varlink service over a Unix socket.
type Conn struct {
	c      net.Conn
	reader *bufio.Reader
}

// Dial connects to the Varlink service listening on the given Unix
// socket path.
func Dial(socketPath string) (*Conn, error) {
	c, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("varlink: dial %s: %w", socketPath, err)
	}
	return &Conn{c: c, reader: bufio.NewReader(c)}, nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.c.Close() }

type callMessage struct {
	Method     string          `json:"method"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type replyMessage struct {
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Continues  bool            `json:"continues,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// CallError is returned when the service replies with an error field
// set -- Parameters holds whatever error-detail payload it included.
type CallError struct {
	Name       string
	Parameters json.RawMessage
}

func (e *CallError) Error() string {
	return fmt.Sprintf("varlink: call error: %s", e.Name)
}

// Call invokes method with params (marshaled to JSON; pass nil for no
// parameters) and unmarshals the reply's parameters into result (pass
// nil to discard them). A service-side error reply is returned as
// *CallError.
func (c *Conn) Call(method string, params any, result any) error {
	var paramsJSON json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("varlink: marshal parameters: %w", err)
		}
		paramsJSON = data
	}

	msg, err := json.Marshal(callMessage{Method: method, Parameters: paramsJSON})
	if err != nil {
		return fmt.Errorf("varlink: marshal call: %w", err)
	}
	msg = append(msg, 0)
	if _, err := c.c.Write(msg); err != nil {
		return fmt.Errorf("varlink: write call: %w", err)
	}

	raw, err := c.reader.ReadBytes(0)
	if err != nil {
		return fmt.Errorf("varlink: read reply: %w", err)
	}
	raw = raw[:len(raw)-1] // drop trailing NUL

	var reply replyMessage
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("varlink: parse reply: %w", err)
	}
	if reply.Error != "" {
		return &CallError{Name: reply.Error, Parameters: reply.Parameters}
	}
	if result != nil && reply.Parameters != nil {
		if err := json.Unmarshal(reply.Parameters, result); err != nil {
			return fmt.Errorf("varlink: unmarshal reply parameters: %w", err)
		}
	}
	return nil
}
