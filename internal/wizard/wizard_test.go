package wizard

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestWriteHostname(t *testing.T) {
	root := t.TempDir()
	if err := writeHostname(root, "box1"); err != nil {
		t.Fatalf("writeHostname: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/hostname"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "box1\n" {
		t.Errorf("got %q, want %q", data, "box1\n")
	}
}

func TestValidationRegexes(t *testing.T) {
	cases := []struct {
		re    *regexp.Regexp
		value string
		want  bool
	}{
		{validDomainRE, "example.com", true},
		{validDomainRE, "not a domain", false},
		{validDomainRE, "sub.example.co", true},
		{validEmailRE, "admin@example.com", true},
		{validEmailRE, "not-an-email", false},
		{validPubkeyRE, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI test@example", true},
		{validPubkeyRE, "ecdsa-sha2-nistp256 AAAA test@example", true},
		{validPubkeyRE, "not a key", false},
	}
	for _, c := range cases {
		got := c.re.MatchString(c.value)
		if got != c.want {
			t.Errorf("MatchString(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}
