// Package quadlet renders Quadlet units + plain config files from the
// existing usr/share/slfhst/templates/*.tmpl files, and creates the
// podman secrets those units reference. Direct port of quadlets.py.
//
// The templates use string.Template's ${VAR}/$VAR syntax; expand.go
// implements a strict expander matching Template.substitute()'s
// behavior (error on any unresolved variable) so the existing template
// files need no changes at all for this port.
package quadlet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/runx"
)

var (
	quadletDir = filepath.Join(config.ServiceHomeDir, ".config/containers/systemd")
	unitDir    = filepath.Join(config.ServiceHomeDir, ".config/systemd/user")
)

// configFileDests maps a rendered (post-.tmpl-stripped) filename to the
// DataDir-relative subdirectory it belongs in.
var configFileDests = map[string]string{
	"traefik-dynamic.yml": "traefik/dynamic",
	"museum.yaml":         "ente",
	"garage.toml":         "garage",
}

var quadletSuffixes = []string{".container", ".network", ".volume"}

// secretEnvNames maps a config.json secret key to the podman secret name
// Quadlet `Secret=` lines reference.
var secretEnvNames = map[string]string{
	"cloudflare_api_token":    "cf_api_token",
	"vaultwarden_admin_token": "vaultwarden_admin_token",
	"postgres_password":       "postgres_password",
	"garage_rpc_secret":       "garage_rpc_secret",
	"garage_admin_token":      "garage_admin_token",
	"stalwart_admin_password": "stalwart_recovery_admin",
}

// secretValueTransform applies the one non-identity transform: Stalwart
// wants its recovery-admin secret as a literal "user:pass" string.
func secretValueTransform(key, value string) string {
	if key == "stalwart_admin_password" {
		return "admin:" + value
	}
	return value
}

// EnsureSecrets creates any podman secret in secretEnvNames that's
// configured (non-empty in cfg) but doesn't already exist.
func EnsureSecrets(cfg map[string]any) error {
	for key, secretName := range secretEnvNames {
		value, _ := cfg[key].(string)
		if value == "" {
			fmt.Fprintf(os.Stderr, "warning: quadlet: no value for secret %q, skipping\n", key)
			continue
		}
		value = secretValueTransform(key, value)

		res, err := runx.Run([]string{"podman", "secret", "inspect", secretName},
			runx.Options{AsUser: config.ServiceUser, Capture: true, NoCheck: true})
		if err != nil {
			return fmt.Errorf("quadlet: check secret %q: %w", secretName, err)
		}
		if res.ExitCode == 0 {
			continue // already exists
		}

		_, err = runx.Run([]string{"podman", "secret", "create", secretName, "-"},
			runx.Options{AsUser: config.ServiceUser, Input: strings.NewReader(value)})
		if err != nil {
			return fmt.Errorf("quadlet: create secret %q: %w", secretName, err)
		}
	}
	return nil
}

// RenderAll renders every usr/share/slfhst/templates/*.tmpl file to its
// resolved destination, then chowns the service user's config +
// DataDir trees and reloads its systemd user manager.
func RenderAll(cfg map[string]any) error {
	vars, err := templateVars(cfg)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(quadletDir, 0o755); err != nil {
		return fmt.Errorf("quadlet: mkdir %s: %w", quadletDir, err)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("quadlet: mkdir %s: %w", unitDir, err)
	}

	entries, err := filepath.Glob(filepath.Join(config.TemplatesDir, "*.tmpl"))
	if err != nil {
		return fmt.Errorf("quadlet: glob templates: %w", err)
	}
	sort.Strings(entries)

	for _, tmplPath := range entries {
		stem := strings.TrimSuffix(filepath.Base(tmplPath), ".tmpl")

		dest, ok := resolveDest(stem)
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: quadlet: no destination rule for %q, skipping\n", stem)
			continue
		}

		text, err := os.ReadFile(tmplPath)
		if err != nil {
			return fmt.Errorf("quadlet: read %s: %w", tmplPath, err)
		}
		rendered, err := strictExpand(string(text), vars)
		if err != nil {
			return fmt.Errorf("quadlet: render %s: %w", stem, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("quadlet: mkdir %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, []byte(rendered), 0o644); err != nil {
			return fmt.Errorf("quadlet: write %s: %w", dest, err)
		}
	}

	if _, err := runx.Run([]string{"chown", "-R", config.ServiceUser + ":" + config.ServiceUser,
		filepath.Join(config.ServiceHomeDir, ".config")}, runx.Options{}); err != nil {
		return fmt.Errorf("quadlet: chown .config: %w", err)
	}
	if _, err := runx.Run([]string{"chown", "-R", config.ServiceUser + ":" + config.ServiceUser,
		config.DataDir}, runx.Options{}); err != nil {
		return fmt.Errorf("quadlet: chown %s: %w", config.DataDir, err)
	}

	if _, err := runx.Run([]string{"machinectl", "shell", config.ServiceUser + "@",
		"/usr/bin/systemctl", "--user", "daemon-reload"}, runx.Options{}); err != nil {
		return fmt.Errorf("quadlet: daemon-reload: %w", err)
	}
	return nil
}

func resolveDest(stem string) (string, bool) {
	if sub, ok := configFileDests[stem]; ok {
		return filepath.Join(config.DataDir, sub, stem), true
	}
	for _, suf := range quadletSuffixes {
		if strings.HasSuffix(stem, suf) {
			return filepath.Join(quadletDir, stem), true
		}
	}
	if strings.HasSuffix(stem, ".target") {
		return filepath.Join(unitDir, stem), true
	}
	return "", false
}

func templateVars(cfg map[string]any) (map[string]string, error) {
	domain, _ := cfg["domain"].(string)
	if domain == "" {
		return nil, fmt.Errorf("quadlet: config.json has no domain set")
	}
	get := func(key string) string {
		v, _ := cfg[key].(string)
		return v
	}
	return map[string]string{
		"DOMAIN":                   domain,
		"MAIL_HOST":                "mail." + domain,
		"VAULT_HOST":               "vault." + domain,
		"PHOTOS_HOST":              "photos." + domain,
		"AUTH_HOST":                "auth." + domain,
		"LOCKER_HOST":              "locker." + domain,
		"API_HOST":                 "api." + domain,
		"ALERT_EMAIL":              get("alert_email"),
		"POSTGRES_PASSWORD":        get("postgres_password"),
		"GARAGE_ADMIN_TOKEN":       get("garage_admin_token"),
		"GARAGE_RPC_SECRET":        get("garage_rpc_secret"),
		"ENTE_ENCRYPTION_KEY":      get("ente_encryption_key"),
		"ENTE_ENCRYPTION_HASH_KEY": get("ente_encryption_hash_key"),
		"ENTE_JWT_SECRET":          get("ente_jwt_secret"),
	}, nil
}
