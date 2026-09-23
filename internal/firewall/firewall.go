// Package firewall covers two different call sites, per the plan's
// "firewalld: two different problems, two different solutions":
//
//   - On the deployed appliance (this file): a live firewalld daemon is
//     running, reached over D-Bus (org.fedoraproject.FirewallD1) via
//     internal/dbus -- this turned out to be firewalld's *primary* API
//     (firewall-cmd itself is just a D-Bus client), confirmed by
//     introspecting the real running daemon, not assumed.
//   - Pre-boot, from the installer against the chrooted/mounted target:
//     no daemon exists to talk to, so that path (not yet written -- plan
//     Phase 3) authors firewalld's on-disk XML directly instead -- also
//     confirmed working (see the plan's Phase 0 notes: hand-authored XML
//     loaded correctly by a real firewalld with no live daemon at write
//     time).
//
// internal/dbus's mechanics (including this exact ipset add/setEntries/
// getEntries round trip) were verified against a real firewalld in a
// container -- see the plan. Plan Phase 2 (this file).
package firewall

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/dbus"
)

const (
	busName     = "org.fedoraproject.FirewallD1"
	rootPath    = "/org/fedoraproject/FirewallD1"
	configPath  = "/org/fedoraproject/FirewallD1/config"
	rootIface   = "org.fedoraproject.FirewallD1"
	configIface = "org.fedoraproject.FirewallD1.config"
	ipsetIface  = "org.fedoraproject.FirewallD1.config.ipset"

	cfIPv4URL = "https://www.cloudflare.com/ips-v4"
	cfIPv6URL = "https://www.cloudflare.com/ips-v6"

	cfIPSetV4 = "cloudflare-v4"
	cfIPSetV6 = "cloudflare-v6"
)

var cfIPSets = []struct {
	name, url, family string
}{
	{cfIPSetV4, cfIPv4URL, "inet"},
	{cfIPSetV6, cfIPv6URL, "inet6"},
}

// SyncCloudflareIPSets refreshes the firewalld ipsets backing the
// Cloudflare-only rich rules on the web port. Direct port of
// firewall.py's sync_cloudflare_ipsets(), D-Bus instead of `firewall-cmd`
// shell-outs: creates each ipset if missing, then replaces its entries
// wholesale via setEntries(as) -- simpler and more atomic than
// firewall.py's per-entry add/remove diff, which existed only to work
// around firewall-cmd's one-entry-at-a-time CLI.
func SyncCloudflareIPSets() error {
	c, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	defer c.Close()

	for _, spec := range cfIPSets {
		ranges, err := fetchCIDRList(spec.url)
		if err != nil {
			return fmt.Errorf("firewall: fetch %s: %w", spec.url, err)
		}

		path, err := ensureIPSet(c, spec.name, spec.family)
		if err != nil {
			return fmt.Errorf("firewall: ensure ipset %s: %w", spec.name, err)
		}

		if err := c.Call(busName, path, ipsetIface, "setEntries", "as", []any{ranges}, "", nil); err != nil {
			return fmt.Errorf("firewall: setEntries on %s: %w", spec.name, err)
		}
	}

	if err := c.Call(busName, rootPath, rootIface, "reload", "", nil, "", nil); err != nil {
		return fmt.Errorf("firewall: reload: %w", err)
	}
	return nil
}

// ensureIPSet returns the object path for an ipset with the given name,
// creating it (hash:net, the given family) if it doesn't exist yet.
func ensureIPSet(c *dbus.Conn, name, family string) (string, error) {
	var path string
	err := c.Call(busName, configPath, configIface, "getIPSetByName", "s", []any{name}, "o", []any{&path})
	if err == nil {
		return path, nil
	}
	if _, ok := err.(*dbus.CallError); !ok {
		return "", err // a real transport/marshal error, not "not found" -- don't paper over it.
	}

	// addIPSet(name, (version, short, description, type, options, entries)).
	settings := []any{"", "", "", "hash:net", map[string]any{"family": family}, []string{}}
	err = c.Call(busName, configPath, configIface, "addIPSet", "s(ssssa{ss}as)", []any{name, settings}, "o", []any{&path})
	if err != nil {
		return "", fmt.Errorf("addIPSet: %w", err)
	}
	return path, nil
}

// fetchCIDRList fetches a newline-separated list of CIDR ranges from
// Cloudflare's published IP-range endpoints.
func fetchCIDRList(url string) ([]string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out, scanner.Err()
}
