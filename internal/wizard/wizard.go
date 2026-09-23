// Package wizard is stage1's interactive first-boot -- now first-install
// -- setup: the only place per-deployment specifics enter the system.
// Direct port of wizard.py, with one change: `hostnamectl set-hostname`
// (a D-Bus call to systemd-hostnamed, no daemon pre-boot) is replaced by
// writing /etc/hostname directly under the target -- that's literally
// one of the things hostnamectl itself does, the rest (pretty hostname
// in /etc/machine-info) isn't used by this project anyway.
package wizard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/prompt"
)

var (
	validPubkeyRE = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-\S+) \S+`)
	validEmailRE  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	validDomainRE = regexp.MustCompile(`^([a-z0-9-]+\.)+[a-z]{2,}$`)
)

// secretKeys get a per-secret prompt (blank = auto-generate) -- direct
// port of wizard.py's SECRET_KEYS. Delivered to containers via `podman
// secret` (see internal/quadlet).
var secretKeys = []string{
	"vaultwarden_admin_token",
	"postgres_password",
	"garage_rpc_secret",
	"garage_admin_token",
	// Plain password -- internal/quadlet wraps it as "admin:<password>"
	// when creating the actual podman secret, the literal
	// STALWART_RECOVERY_ADMIN format Stalwart expects.
	"stalwart_admin_password",
}

// fileSecretKeys are never prompted for -- always auto-generated. Ente
// museum only accepts these embedded in museum.yaml, not as env vars, so
// they're kept in config.json like the rest of the wizard's answers.
var fileSecretKeys = []string{"ente_encryption_key", "ente_encryption_hash_key", "ente_jwt_secret"}

// Run collects the interactive answers, writes them to config.json under
// store.Root, and writes /etc/hostname under the target. Returns the
// resulting config for callers (installer.go) that need it immediately
// without a second Load().
func Run(store config.Store) (map[string]any, error) {
	prompt.Banner("slfhst first-boot setup")
	fmt.Println("This box will become a self-hosted mail + photos/auth/locker + vault")
	fmt.Println("appliance. Answers below are only asked once.")
	fmt.Println()

	hostname, err := prompt.Ask("Hostname (short, e.g. box1)", prompt.Options{})
	if err != nil {
		return nil, err
	}
	domain, err := prompt.Ask("Domain (must already be on Cloudflare)", prompt.Options{Validate: validDomainRE.MatchString})
	if err != nil {
		return nil, err
	}
	adminUser, err := prompt.Ask("Admin username", prompt.Options{Default: "admin"})
	if err != nil {
		return nil, err
	}

	// Key is optional -- a password is always set for this account too
	// (see internal/totp, internal/users), so a lost/unavailable key is
	// never a lockout.
	pubkey, err := prompt.Ask("Admin SSH public key (paste the full line, blank to skip)",
		prompt.Options{Optional: true, Validate: validPubkeyRE.MatchString})
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("Admin account password (used for SSH login when no key is presented --")
	fmt.Println("TOTP is required either way). Blank = auto-generate and print once.")
	adminPassword, err := prompt.AskSecret("Password")
	if err != nil {
		return nil, err
	}
	if adminPassword != "" {
		confirm, err := prompt.AskSecret("Confirm")
		if err != nil {
			return nil, err
		}
		if confirm != adminPassword {
			return nil, fmt.Errorf("wizard: passwords didn't match")
		}
	} else {
		adminPassword, err = config.GenSecret(12)
		if err != nil {
			return nil, err
		}
		fmt.Printf("Generated admin password (write this down, shown once): %s\n", adminPassword)
	}

	alertEmail, err := prompt.Ask("Email address for alerts (delivered via this box's own mail server)",
		prompt.Options{Validate: validEmailRE.MatchString})
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("Cloudflare API token (Zone:DNS Edit scope for the domain above).")
	fmt.Println("Input is hidden.")
	var cfToken string
	for cfToken == "" {
		cfToken, err = prompt.AskSecret("Cloudflare API token")
		if err != nil {
			return nil, err
		}
	}

	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}
	cfg["hostname"] = hostname
	cfg["domain"] = domain
	cfg["admin_user"] = adminUser
	cfg["admin_pubkey"] = pubkey
	cfg["admin_password"] = adminPassword
	cfg["alert_email"] = alertEmail
	// Long-lived credential, kept alongside the other secrets in the
	// root-only (0600) config.json. Turned into a real `podman secret`
	// in stage2 once the service user + its podman storage exist (see
	// internal/quadlet) -- creating it here would land in the wrong
	// (root) podman namespace, not the rootless svc one.
	cfg["cloudflare_api_token"] = cfToken

	fmt.Println()
	fmt.Println("Per-service secrets: leave blank to auto-generate (recommended).")
	for _, key := range secretKeys {
		label := strings.ReplaceAll(key, "_", " ")
		value, err := prompt.Ask(fmt.Sprintf("%s (blank = auto-generate)", label), prompt.Options{Optional: true})
		if err != nil {
			return nil, err
		}
		if value == "" {
			value, err = config.GenSecret(0)
			if err != nil {
				return nil, err
			}
		}
		cfg[key] = value
	}

	// No prompts for these -- always auto-generated, never worth typing.
	for _, key := range fileSecretKeys {
		if _, ok := cfg[key]; !ok {
			value, err := config.GenSecret(0)
			if err != nil {
				return nil, err
			}
			cfg[key] = value
		}
	}

	if err := store.Save(cfg); err != nil {
		return nil, err
	}
	if err := writeHostname(store.Root, hostname); err != nil {
		return nil, err
	}

	prompt.Banner("Setup captured.")
	return cfg, nil
}

func writeHostname(root, hostname string) error {
	path := filepath.Join(root, "etc/hostname")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("wizard: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(hostname+"\n"), 0o644); err != nil {
		return fmt.Errorf("wizard: write %s: %w", path, err)
	}
	return nil
}
