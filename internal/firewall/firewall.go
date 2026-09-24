// Package firewall covers the live appliance (this file, over D-Bus)
// and the pre-boot installer target (preboot.go, on-disk XML).
package firewall

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
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

	// cfIPListDir holds CI-fetched Cloudflare IP ranges baked into /usr
	// at build time (see mkosi/build-all.sh), not fetched live.
	cfIPListDir = "usr/share/slfhst/cloudflare-ips"

	cfIPSetV4 = "cloudflare-v4"
	cfIPSetV6 = "cloudflare-v6"
)

var cfIPSets = []struct {
	name, file, family string
}{
	{cfIPSetV4, "v4.txt", "inet"},
	{cfIPSetV6, "v6.txt", "inet6"},
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
		ranges, err := readCIDRList(filepath.Join("/", cfIPListDir, spec.file))
		if err != nil {
			return fmt.Errorf("firewall: read %s: %w", spec.file, err)
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

// readCIDRList reads a newline-separated list of CIDR ranges from a
// bundled file (see cfIPListDir).
func readCIDRList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out, scanner.Err()
}
