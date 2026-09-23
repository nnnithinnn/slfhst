package prompt

import (
	"bufio"
	"strings"
	"testing"
)

func withInput(t *testing.T, input string, fn func()) {
	t.Helper()
	old := stdin
	stdin = bufio.NewReader(strings.NewReader(input))
	defer func() { stdin = old }()
	fn()
}

func TestAskPlain(t *testing.T) {
	withInput(t, "hello\n", func() {
		got, err := Ask("label", Options{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if got != "hello" {
			t.Errorf("got %q, want hello", got)
		}
	})
}

func TestAskDefault(t *testing.T) {
	withInput(t, "\n", func() {
		got, err := Ask("label", Options{Default: "admin"})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if got != "admin" {
			t.Errorf("got %q, want admin", got)
		}
	})
}

func TestAskOptionalBlank(t *testing.T) {
	withInput(t, "\n", func() {
		got, err := Ask("label", Options{Optional: true})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestAskValidateRetries(t *testing.T) {
	// First line fails validation, second passes.
	withInput(t, "bad\ngood\n", func() {
		got, err := Ask("label", Options{Validate: func(s string) bool { return s == "good" }})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if got != "good" {
			t.Errorf("got %q, want good", got)
		}
	})
}

func TestAskRequiredRetriesOnBlank(t *testing.T) {
	withInput(t, "\nvalue\n", func() {
		got, err := Ask("label", Options{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if got != "value" {
			t.Errorf("got %q, want value", got)
		}
	})
}
