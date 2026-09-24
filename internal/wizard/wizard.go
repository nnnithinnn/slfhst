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

var secretKeys = []string{
	"vaultwarden_admin_token",
	"postgres_password",
	"garage_rpc_secret",
	"garage_admin_token",
	"stalwart_admin_password",
}

var fileSecretKeys = []string{"ente_encryption_key", "ente_encryption_hash_key", "ente_jwt_secret"}

// RunPreBoot collects hostname and admin account details at the
// installer console, before the target has networking or SSH.
func RunPreBoot(store config.Store) (map[string]any, error) {
	prompt.Banner("slfhst installer setup")

	hostname, err := prompt.Ask("Hostname (short, e.g. box1)", prompt.Options{})
	if err != nil {
		return nil, err
	}
	adminUser, err := prompt.Ask("Admin username", prompt.Options{Default: "admin"})
	if err != nil {
		return nil, err
	}
	pubkey, err := prompt.Ask("Admin SSH public key (paste the full line, blank to skip)",
		prompt.Options{Optional: true, Validate: validPubkeyRE.MatchString})
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("Admin account password (used to SSH in after reboot).")
	fmt.Println("Blank = auto-generate and print once.")
	adminPassword, err := askPassword()
	if err != nil {
		return nil, err
	}

	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}
	cfg["hostname"] = hostname
	cfg["admin_user"] = adminUser
	cfg["admin_pubkey"] = pubkey
	cfg["admin_password"] = adminPassword

	if err := store.Save(cfg); err != nil {
		return nil, err
	}
	if err := writeHostname(store.Root, hostname); err != nil {
		return nil, err
	}

	prompt.Banner("Setup captured.")
	return cfg, nil
}

// RunPostBoot collects domain, alert email, Cloudflare token, and
// per-service secrets over SSH after first boot -- idempotent, skips
// prompting if this box has already been through `slfhst setup` once.
func RunPostBoot(store config.Store) (map[string]any, error) {
	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}
	if domain, _ := cfg["domain"].(string); domain != "" {
		return cfg, nil
	}

	prompt.Banner("slfhst setup")

	domain, err := prompt.Ask("Domain (must already be on Cloudflare)", prompt.Options{Validate: validDomainRE.MatchString})
	if err != nil {
		return nil, err
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

	cfg["domain"] = domain
	cfg["alert_email"] = alertEmail
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

	prompt.Banner("Setup captured.")
	return cfg, nil
}

func askPassword() (string, error) {
	for {
		password, err := prompt.AskSecret("Password")
		if err != nil {
			return "", err
		}
		if password == "" {
			generated, err := config.GenSecret(12)
			if err != nil {
				return "", err
			}
			fmt.Printf("Generated admin password (write this down, shown once): %s\n", generated)
			return generated, nil
		}
		confirm, err := prompt.AskSecret("Confirm")
		if err != nil {
			return "", err
		}
		if confirm == password {
			return password, nil
		}
		fmt.Println("passwords didn't match, try again")
	}
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
