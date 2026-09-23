// Package healthcheck is stage2's final sanity check + MOTD banner.
// Direct port of healthcheck.py.
package healthcheck

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/monitor"
)

const motdFile = "/etc/motd.d/slfhst.motd"

// writeMOTD writes the service-URL banner shown at SSH login. Direct
// port of healthcheck.py's write_motd() -- note api.<domain> (Ente's
// backend) is deliberately not listed, matching the Python original.
func writeMOTD(cfg map[string]any) error {
	domain, _ := cfg["domain"].(string)
	lines := fmt.Sprintf(`slfhst -- self-hosted mail + photos/auth/locker + vault

  Mail:   https://mail.%[1]s
  Vault:  https://vault.%[1]s
  Photos: https://photos.%[1]s
  Auth:   https://auth.%[1]s
  Locker: https://locker.%[1]s

TOTP enrollment was shown once on the console during setup and isn't
stored anywhere else -- if you lost it, re-enroll via google-authenticator.
`, domain)

	if err := os.MkdirAll(filepath.Dir(motdFile), 0o755); err != nil {
		return fmt.Errorf("healthcheck: mkdir %s: %w", filepath.Dir(motdFile), err)
	}
	if err := os.WriteFile(motdFile, []byte(lines), 0o644); err != nil {
		return fmt.Errorf("healthcheck: write %s: %w", motdFile, err)
	}
	return nil
}

// Run is stage2's final step: log which app services aren't active yet
// (a warning, not a failure -- containers can still be starting), then
// write the MOTD. Direct port of healthcheck.py's main().
func Run(store config.Store) error {
	if err := store.RequireDone("stage1"); err != nil {
		return err
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}

	statuses := monitor.ServiceStatus()
	var notActive []string
	for name, status := range statuses {
		if status != "active" {
			notActive = append(notActive, fmt.Sprintf("%s=%s", name, status))
		}
	}
	if len(notActive) > 0 {
		fmt.Println("warning: healthcheck: not all app services are active yet:", notActive)
	} else {
		fmt.Println("healthcheck: all app services active")
	}

	return writeMOTD(cfg)
}
