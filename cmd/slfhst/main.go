// Command slfhst is the single codebase for this appliance: installer,
// stage2 bootstrap, OS updates, and ongoing ops/monitoring all live in
// one binary, matching the user's explicit "one CLI codebase" correction
// to the original per-concern-binary draft. See
// /home/ubuntu/.claude/plans/agile-kindling-cray.md for the full design.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/nnnithinnn/slfhst/internal/bootstrap"
	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/dnscf"
	"github.com/nnnithinnn/slfhst/internal/firewall"
	"github.com/nnnithinnn/slfhst/internal/installer"
	"github.com/nnnithinnn/slfhst/internal/monitor"
	"github.com/nnnithinnn/slfhst/internal/sysupdate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, config.ErrNotDone) {
			// Matches common.py's require_done_or_exit: a missing
			// prerequisite stage is a clean no-op, not a failure.
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "slfhst:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	if len(argv) == 0 {
		return usageError("")
	}

	store := config.Store{} // live system; the installer overrides this internally against its target.

	switch argv[0] {
	case "install":
		return installer.Run()
	case "bootstrap":
		return bootstrap.Run(store)
	case "status":
		return monitor.Status(store)
	case "check-all":
		return monitor.CheckAll(store)
	case "dns":
		return dispatchDNS(store, argv[1:])
	case "cf-ips":
		return dispatchCFIPs(store, argv[1:])
	case "update":
		return dispatchUpdate(argv[1:])
	default:
		return usageError(argv[0])
	}
}

func dispatchDNS(store config.Store, argv []string) error {
	if len(argv) != 1 || argv[0] != "sync" {
		return usageError("dns")
	}
	return dnscf.Sync(store)
}

func dispatchCFIPs(store config.Store, argv []string) error {
	if len(argv) != 1 || argv[0] != "sync" {
		return usageError("cf-ips")
	}
	_ = store // firewall.py's Cloudflare-ipset sync doesn't read config.json either.
	return firewall.SyncCloudflareIPSets()
}

func dispatchUpdate(argv []string) error {
	if len(argv) != 1 {
		return usageError("update")
	}
	switch argv[0] {
	case "check":
		return sysupdate.Check()
	case "apply":
		return sysupdate.Apply()
	default:
		return usageError("update")
	}
}

func usageError(sub string) error {
	const usage = `usage: slfhst <command>

  install            run the interactive live-installer flow
  bootstrap          stage2: render quadlets, start containers, bootstrap
                      backing services, sync DNS (admin-run over SSH)
  status              service status summary
  check-all           periodic health/DNSBL/anomaly-email check
  dns sync            sync Cloudflare DNS records
  cf-ips sync          refresh the firewalld Cloudflare ipsets
  update check|apply   check for / apply an OS update via systemd-sysupdate
`
	if sub == "" {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", sub)
}
