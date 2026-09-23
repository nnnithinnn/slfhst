// Pre-boot firewall setup: firewalld's on-disk XML, authored directly
// against the installer's mounted target -- no live daemon exists yet to
// talk to (unlike SyncCloudflareIPSets in firewall.go, which runs
// post-boot against a real daemon over D-Bus). Confirmed working for
// real (plan Phase 0): XML written with no daemon present at write time
// was loaded correctly by a real firewalld, `firewall-cmd --list-all`
// matched intent exactly, including ipset entries and a Cloudflare rich
// rule. Direct port of firewall.py's setup(), stage1-only in the
// original too -- there's no live-daemon equivalent of this function to
// keep in sync, `setup()` was never called from the ongoing-ops CLI
// surface.
package firewall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mailForwards are (public port, container-side port) pairs -- no
// plaintext/STARTTLS submission(587)/imap(143) forwards, since Stalwart
// v0.16 drops those listeners by default.
var mailForwards = []struct{ public, private int }{
	{25, 2525}, {465, 4465}, {993, 4993},
}

const manageSievePort = 4190

const ( // web: forward-only, no direct port -- only reachable via the Cloudflare-ipset rich rule below.
	webPublicPort  = 443
	webPrivatePort = 8443
)

// mailRateLimits are new work (not a port of anything in firewall.py):
// per-port connection-rate limits on the directly-internet-exposed mail
// ports, since Cloudflare doesn't proxy raw SMTP/IMAPS the way it does
// the web port -- see the plan's "DDoS / abuse hardening" section.
var mailRateLimits = []struct {
	port  int
	limit string
}{
	{25, "20/m"}, {465, "20/m"}, {993, "20/m"}, {manageSievePort, "20/m"},
}

// SetupPreBoot authors firewalld's permanent config directly under
// root/etc/firewalld/ -- the pre-boot equivalent of firewall.py's
// setup(), run once by the installer before reboot.
func SetupPreBoot(root string) error {
	v4ranges, err := fetchCIDRList(cfIPv4URL)
	if err != nil {
		return fmt.Errorf("firewall: fetch %s: %w", cfIPv4URL, err)
	}
	v6ranges, err := fetchCIDRList(cfIPv6URL)
	if err != nil {
		return fmt.Errorf("firewall: fetch %s: %w", cfIPv6URL, err)
	}

	if err := writeIPSetXML(root, cfIPSetV4, "inet", v4ranges); err != nil {
		return err
	}
	if err := writeIPSetXML(root, cfIPSetV6, "inet6", v6ranges); err != nil {
		return err
	}
	return writeZoneXML(root)
}

func writeIPSetXML(root, name, family string, entries []string) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString("<ipset type=\"hash:net\">\n")
	fmt.Fprintf(&b, "  <option name=\"family\" value=\"%s\"/>\n", family)
	for _, entry := range entries {
		fmt.Fprintf(&b, "  <entry>%s</entry>\n", entry)
	}
	b.WriteString("</ipset>\n")

	path := filepath.Join(root, "etc/firewalld/ipsets", name+".xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("firewall: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("firewall: write %s: %w", path, err)
	}
	return nil
}

func writeZoneXML(root string) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString("<zone>\n")
	b.WriteString("  <short>Public</short>\n")
	b.WriteString("  <description>slfhst: public zone, ports scoped to what this appliance actually serves.</description>\n")
	b.WriteString("  <service name=\"ssh\"/>\n")

	for _, fw := range mailForwards {
		fmt.Fprintf(&b, "  <port port=\"%d\" protocol=\"tcp\"/>\n", fw.public)
		fmt.Fprintf(&b, "  <forward-port port=\"%d\" protocol=\"tcp\" to-port=\"%d\"/>\n", fw.public, fw.private)
	}
	fmt.Fprintf(&b, "  <port port=\"%d\" protocol=\"tcp\"/>\n", manageSievePort)
	fmt.Fprintf(&b, "  <forward-port port=\"%d\" protocol=\"tcp\" to-port=\"%d\"/>\n", webPublicPort, webPrivatePort)

	// Web port: only reachable through Cloudflare's edge.
	for _, family := range []struct{ name, ipset string }{{"ipv4", cfIPSetV4}, {"ipv6", cfIPSetV6}} {
		fmt.Fprintf(&b, "  <rule family=\"%s\">\n", family.name)
		fmt.Fprintf(&b, "    <source ipset=\"%s\"/>\n", family.ipset)
		fmt.Fprintf(&b, "    <port port=\"%d\" protocol=\"tcp\"/>\n", webPrivatePort)
		b.WriteString("    <accept/>\n")
		b.WriteString("  </rule>\n")
	}

	// Mail ports: directly internet-exposed (Cloudflare doesn't proxy
	// raw SMTP/IMAPS), so rate-limit instead of ipset-restrict.
	for _, family := range []string{"ipv4", "ipv6"} {
		for _, rl := range mailRateLimits {
			fmt.Fprintf(&b, "  <rule family=\"%s\">\n", family)
			fmt.Fprintf(&b, "    <port port=\"%d\" protocol=\"tcp\"/>\n", rl.port)
			fmt.Fprintf(&b, "    <accept>\n      <limit value=\"%s\"/>\n    </accept>\n", rl.limit)
			b.WriteString("  </rule>\n")
		}
	}

	b.WriteString("  <forward/>\n")
	b.WriteString("</zone>\n")

	path := filepath.Join(root, "etc/firewalld/zones/public.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("firewall: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("firewall: write %s: %w", path, err)
	}
	return nil
}
