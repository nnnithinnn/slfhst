// Package netconf is stage1's interactive network setup: pick a NIC,
// DHCP or static (IPv4 + optional multiple IPv6), write the
// systemd-networkd/-resolved config. Direct port of netconf.py, with one
// deliberate behavior change: this now runs from the installer against
// the target's mounted filesystem, before the target ever boots, so it
// is file-generation only -- the `systemctl restart systemd-networkd`/
// `-resolved` calls netconf.py made are gone; systemd-networkd picks up
// the written config automatically the first time the target actually
// boots. Interface enumeration still runs live (against the installer's
// own running kernel/hardware, not the target, which has none yet).
package netconf

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/prompt"
	"github.com/nnnithinnn/slfhst/internal/runx"
)

var excludedPrefixes = []string{"lo", "veth", "podman", "docker", "cni", "br-", "virbr"}

var ifaceLineRE = regexp.MustCompile(`^\d+:\s+([^:@]+)[:@]`)

// ListInterfaces enumerates real network interfaces via `ip -o link
// show`, excluding loopback/virtual/container-managed ones. Runs
// against the live installer environment, not any mounted target.
func ListInterfaces() ([]string, error) {
	out, err := runx.RunChecked([]string{"ip", "-o", "link", "show"}, runx.Options{})
	if err != nil {
		return nil, fmt.Errorf("netconf: list interfaces: %w", err)
	}
	var ifaces []string
	for _, line := range strings.Split(out, "\n") {
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
			ifaces = append(ifaces, name)
		}
	}
	return ifaces, nil
}

type staticConfig struct {
	IPv4Address   string   `json:"ipv4_address,omitempty"`
	IPv4Gateway   string   `json:"ipv4_gateway,omitempty"`
	IPv6Addresses []string `json:"ipv6_addresses,omitempty"`
	IPv6Gateway   string   `json:"ipv6_gateway,omitempty"`
}

// Configure interactively asks for DHCP-vs-static network setup, writes
// the resulting systemd-networkd/-resolved config under store.Root, and
// records the choice in config.json.
func Configure(store config.Store) error {
	ifaces, err := ListInterfaces()
	if err != nil {
		return err
	}
	if len(ifaces) == 0 {
		return fmt.Errorf("netconf: no candidate network interfaces found")
	}
	iface := ifaces[0]
	if len(ifaces) > 1 {
		prompt.Banner("Multiple network interfaces found")
		for i, name := range ifaces {
			fmt.Printf("  %d) %s\n", i+1, name)
		}
		choice, err := prompt.Ask(fmt.Sprintf("Which interface? (1-%d)", len(ifaces)), prompt.Options{Default: "1"})
		if err != nil {
			return err
		}
		idx := indexOrDefault(choice, 1) - 1
		if idx < 0 || idx >= len(ifaces) {
			idx = 0
		}
		iface = ifaces[idx]
	}

	mode, err := prompt.Ask("DHCP or static?", prompt.Options{Default: "dhcp"})
	if err != nil {
		return err
	}

	var static *staticConfig
	if strings.EqualFold(mode, "static") {
		static, err = askStatic()
		if err != nil {
			return err
		}
	}

	if err := writeNetworkFile(store.Root, iface, static); err != nil {
		return err
	}
	if err := writeResolvedConf(store.Root); err != nil {
		return err
	}
	if err := fixResolvConfSymlink(store.Root); err != nil {
		return err
	}

	if static != nil {
		return store.Update(map[string]any{"network": map[string]any{
			"ipv4_address":   static.IPv4Address,
			"ipv4_gateway":   static.IPv4Gateway,
			"ipv6_addresses": static.IPv6Addresses,
			"ipv6_gateway":   static.IPv6Gateway,
		}})
	}
	return store.Update(map[string]any{"network": map[string]any{"mode": "dhcp"}})
}

func indexOrDefault(s string, def int) int {
	n := def
	fmt.Sscanf(s, "%d", &n)
	return n
}

var ipv4CIDRRE = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}/\d{1,2}$`)
var ipv4RE = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)
var ipv6CIDRRE = regexp.MustCompile(`^[0-9a-fA-F:]+/\d{1,3}$`)
var ipv6RE = regexp.MustCompile(`^[0-9a-fA-F:]+$`)

// stripWhitespace removes embedded whitespace a pasted address can pick
// up from a wrapped terminal/dashboard -- no valid IP/CIDR ever
// contains whitespace, so this is always safe.
func stripWhitespace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func askStatic() (*staticConfig, error) {
	sc := &staticConfig{}

	v4addr, err := prompt.Ask("IPv4 address/CIDR (e.g. 203.0.113.7/24)", prompt.Options{
		Normalize: stripWhitespace,
		Validate:  ipv4CIDRRE.MatchString,
	})
	if err != nil {
		return nil, err
	}
	sc.IPv4Address = v4addr

	v4gw, err := prompt.Ask("IPv4 gateway", prompt.Options{Normalize: stripWhitespace, Validate: ipv4RE.MatchString})
	if err != nil {
		return nil, err
	}
	sc.IPv4Gateway = v4gw

	for {
		v6, err := prompt.Ask("IPv6 address/CIDR (blank to stop, e.g. 2001:db8::7/64)", prompt.Options{
			Optional:  true,
			Normalize: stripWhitespace,
			Validate:  ipv6CIDRRE.MatchString,
		})
		if err != nil {
			return nil, err
		}
		if v6 == "" {
			break
		}
		sc.IPv6Addresses = append(sc.IPv6Addresses, v6)
	}
	if len(sc.IPv6Addresses) > 0 {
		v6gw, err := prompt.Ask("IPv6 gateway", prompt.Options{Normalize: stripWhitespace, Validate: ipv6RE.MatchString})
		if err != nil {
			return nil, err
		}
		sc.IPv6Gateway = v6gw
	}

	if sc.IPv4Address == "" && len(sc.IPv6Addresses) == 0 {
		return nil, nil // nothing entered -- fall back to DHCP, matching netconf.py.
	}
	return sc, nil
}

func writeNetworkFile(root, iface string, static *staticConfig) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[Match]\nName=%s\n\n[Network]\n", iface)

	if static == nil {
		b.WriteString("DHCP=yes\n\n[DHCP]\nUseDNS=no\n")
	} else {
		b.WriteString("DHCP=no\n")
		if len(static.IPv6Addresses) > 0 {
			b.WriteString("IPv6AcceptRA=no\n")
		}
		if static.IPv4Address != "" {
			fmt.Fprintf(&b, "Address=%s\n", static.IPv4Address)
		}
		for _, addr := range static.IPv6Addresses {
			fmt.Fprintf(&b, "Address=%s\n", addr)
		}
		if static.IPv4Gateway != "" {
			fmt.Fprintf(&b, "\n[Route]\nGateway=%s\nGatewayOnLink=yes\n", static.IPv4Gateway)
		}
		if static.IPv6Gateway != "" {
			fmt.Fprintf(&b, "\n[Route]\nGateway=%s\nGatewayOnLink=yes\n", static.IPv6Gateway)
		}
	}

	path := filepath.Join(root, "etc/systemd/network/20-wan.network")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("netconf: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("netconf: write %s: %w", path, err)
	}
	return nil
}

func writeResolvedConf(root string) error {
	const content = `[Resolve]
DNS=9.9.9.9#dns.quad9.net 2620:fe::fe#dns.quad9.net
DNSOverTLS=yes
DNSSEC=yes
FallbackDNS=
`
	path := filepath.Join(root, "etc/systemd/resolved.conf.d/quad9.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("netconf: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("netconf: write %s: %w", path, err)
	}
	return nil
}

func fixResolvConfSymlink(root string) error {
	path := filepath.Join(root, "etc/resolv.conf")
	if _, err := os.Lstat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("netconf: remove existing %s: %w", path, err)
		}
	}
	if err := os.Symlink("/run/systemd/resolve/stub-resolv.conf", path); err != nil {
		return fmt.Errorf("netconf: symlink %s: %w", path, err)
	}
	return nil
}
