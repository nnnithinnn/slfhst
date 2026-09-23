package netconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNetworkFileDHCP(t *testing.T) {
	root := t.TempDir()
	if err := writeNetworkFile(root, "eth0", nil); err != nil {
		t.Fatalf("writeNetworkFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/systemd/network/20-wan.network"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	for _, want := range []string{"Name=eth0", "DHCP=yes", "UseDNS=no"} {
		if !strings.Contains(text, want) {
			t.Errorf("DHCP .network file missing %q:\n%s", want, text)
		}
	}
}

func TestWriteNetworkFileStatic(t *testing.T) {
	root := t.TempDir()
	static := &staticConfig{
		IPv4Address:   "203.0.113.7/24",
		IPv4Gateway:   "203.0.113.1",
		IPv6Addresses: []string{"2001:db8::7/64"},
		IPv6Gateway:   "2001:db8::1",
	}
	if err := writeNetworkFile(root, "eth0", static); err != nil {
		t.Fatalf("writeNetworkFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/systemd/network/20-wan.network"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"Name=eth0", "DHCP=no", "IPv6AcceptRA=no",
		"Address=203.0.113.7/24", "Address=2001:db8::7/64",
		"Gateway=203.0.113.1", "Gateway=2001:db8::1", "GatewayOnLink=yes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("static .network file missing %q:\n%s", want, text)
		}
	}
}

func TestWriteResolvedConf(t *testing.T) {
	root := t.TempDir()
	if err := writeResolvedConf(root); err != nil {
		t.Fatalf("writeResolvedConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/systemd/resolved.conf.d/quad9.conf"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "9.9.9.9#dns.quad9.net") {
		t.Errorf("quad9.conf missing DNS= line:\n%s", data)
	}
}

func TestFixResolvConfSymlink(t *testing.T) {
	root := t.TempDir()
	etcDir := filepath.Join(root, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing plain file (the common real-world case pre-network-setup).
	if err := os.WriteFile(filepath.Join(etcDir, "resolv.conf"), []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := fixResolvConfSymlink(root); err != nil {
		t.Fatalf("fixResolvConfSymlink: %v", err)
	}

	link := filepath.Join(etcDir, "resolv.conf")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected a symlink, Readlink failed: %v", err)
	}
	if target != "/run/systemd/resolve/stub-resolv.conf" {
		t.Errorf("symlink target = %q, want /run/systemd/resolve/stub-resolv.conf", target)
	}
}

func TestListInterfacesExcludesVirtual(t *testing.T) {
	// Not mocking `ip` itself (that's an installer-environment concern,
	// exercised for real against the container, not here) -- this just
	// confirms the exclusion-prefix logic reads real `ip -o link show`
	// output shaped output correctly via a table-driven check of the
	// underlying regex+prefix logic.
	lines := []string{
		"1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536",
		"2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500",
		"3: podman0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500",
		"4: veth1234@if3: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500",
	}
	var got []string
	for _, line := range lines {
		m := ifaceLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		excluded := false
		for _, p := range excludedPrefixes {
			if strings.HasPrefix(name, p) {
				excluded = true
				break
			}
		}
		if !excluded {
			got = append(got, name)
		}
	}
	if len(got) != 1 || got[0] != "eth0" {
		t.Errorf("got %v, want [eth0]", got)
	}
}
