package varlink

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// startFakeService listens on a Unix socket and replies to every call
// with a canned reply, echoing back whatever "id" parameter it received.
// This only exercises the wire framing (NUL-delimited JSON) that this
// package hand-rolls -- it says nothing about any real systemd
// interface's actual method/parameter shapes, which is exactly why the
// plan treats those as a separate Phase 0 spike.
func startFakeService(t *testing.T) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for {
			raw, err := r.ReadBytes(0)
			if err != nil {
				return
			}
			raw = raw[:len(raw)-1]

			var call struct {
				Method     string `json:"method"`
				Parameters struct {
					ID string `json:"id"`
				} `json:"parameters"`
			}
			if err := json.Unmarshal(raw, &call); err != nil {
				return
			}

			var reply []byte
			if call.Parameters.ID == "boom" {
				reply, _ = json.Marshal(map[string]any{
					"error":      "org.example.Failed",
					"parameters": map[string]any{"reason": "boom requested"},
				})
			} else {
				reply, _ = json.Marshal(map[string]any{
					"parameters": map[string]any{"echoedId": call.Parameters.ID, "method": call.Method},
				})
			}
			reply = append(reply, 0)
			if _, err := conn.Write(reply); err != nil {
				return
			}
		}
	}()

	return sockPath
}

func TestCallRoundTrip(t *testing.T) {
	sockPath := startFakeService(t)

	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	var result struct {
		EchoedID string `json:"echoedId"`
		Method   string `json:"method"`
	}
	err = conn.Call("org.example.Ping", map[string]string{"id": "hello"}, &result)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.EchoedID != "hello" || result.Method != "org.example.Ping" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCallError(t *testing.T) {
	sockPath := startFakeService(t)

	conn, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	err = conn.Call("org.example.Ping", map[string]string{"id": "boom"}, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var callErr *CallError
	if ok := errors.As(err, &callErr); !ok {
		t.Fatalf("expected *CallError, got %T: %v", err, err)
	}
	if callErr.Name != "org.example.Failed" {
		t.Fatalf("callErr.Name = %q, want org.example.Failed", callErr.Name)
	}
}

func TestDialNoSuchSocket(t *testing.T) {
	_, err := Dial(filepath.Join(os.TempDir(), "does-not-exist.sock"))
	if err == nil {
		t.Fatal("expected an error dialing a nonexistent socket")
	}
}
