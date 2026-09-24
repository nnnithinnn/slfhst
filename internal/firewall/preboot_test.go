package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupPreBootReadsBundledIPListsNotNetwork is a regression test for
// a real bug found on real hardware: SetupPreBoot used to fetch
// Cloudflare's IP ranges live over HTTPS, which can never succeed in
// the installer's offline environment and aborted the whole install
// right after the wizard. It now reads CI-fetched files bundled into
// the appliance's /usr content instead.
func TestSetupPreBootReadsBundledIPListsNotNetwork(t *testing.T) {
	root := t.TempDir()
	cfDir := filepath.Join(root, cfIPListDir)
	if err := os.MkdirAll(cfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "v4.txt"), []byte("203.0.113.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "v6.txt"), []byte("2001:db8::/32\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetupPreBoot(root); err != nil {
		t.Fatalf("SetupPreBoot: %v", err)
	}

	v4xml, err := os.ReadFile(filepath.Join(root, "etc/firewalld/ipsets", cfIPSetV4+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v4xml), "203.0.113.0/24") {
		t.Errorf("ipv4 ipset XML missing bundled entry:\n%s", v4xml)
	}

	v6xml, err := os.ReadFile(filepath.Join(root, "etc/firewalld/ipsets", cfIPSetV6+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v6xml), "2001:db8::/32") {
		t.Errorf("ipv6 ipset XML missing bundled entry:\n%s", v6xml)
	}
}

func TestSetupPreBootFailsClearlyWhenBundledListsMissing(t *testing.T) {
	root := t.TempDir()
	if err := SetupPreBoot(root); err == nil {
		t.Fatal("expected an error when the bundled Cloudflare IP files don't exist")
	}
}

func TestWriteZoneXMLIncludesSSHRateLimit(t *testing.T) {
	root := t.TempDir()
	if err := writeZoneXML(root); err != nil {
		t.Fatalf("writeZoneXML: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/firewalld/zones/public.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `<service name="ssh"/>`) {
		t.Errorf("zone XML should not have an unconditional ssh service accept")
	}
	if !strings.Contains(string(data), `priority="-10"`) {
		t.Errorf("SSH rate-limit rule missing negative priority:\n%s", data)
	}
}
