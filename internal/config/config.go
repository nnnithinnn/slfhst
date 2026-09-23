// Package config is the shared foundation every other slfhst package
// depends on: paths, config.json load/save, and the marker-file
// idempotency convention (mark done / is done / require done or exit).
//
// Every operation is rooted through a Store so the same code works both
// on the live appliance (Root == "") and from the installer against an
// unbooted target mounted at e.g. /mnt/target (Root == "/mnt/target").
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	StateDir       = "/var/lib/slfhst"
	ConfigDir      = "/etc/slfhst"
	ConfigFile     = ConfigDir + "/config.json"
	TemplatesDir   = "/usr/share/slfhst/templates"
	DataDir        = "/srv/data"
	ServiceUser    = "svc"
	ServiceHomeDir = "/home/svc"
)

// Store reads and writes slfhst's on-disk state under an optional root
// prefix.
type Store struct {
	// Root is prepended to every path. Empty means the live filesystem.
	Root string
}

func (s Store) path(p string) string {
	if s.Root == "" {
		return p
	}
	return filepath.Join(s.Root, p)
}

// Load reads config.json, returning an empty map if it doesn't exist yet
// (matches common.py's load_config: no error on a fresh system).
func (s Store) Load() (map[string]any, error) {
	data, err := os.ReadFile(s.path(ConfigFile))
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", ConfigFile, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", ConfigFile, err)
	}
	return cfg, nil
}

// Save writes cfg as indent=2, sort_keys-equivalent JSON (Go's
// json.Marshal on a map[string]any already sorts keys) and chmods the
// file 0600 -- config.json holds secrets in plaintext.
func (s Store) Save(cfg map[string]any) error {
	dir := s.path(ConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	data = append(data, '\n')
	path := s.path(ConfigFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("config: chmod %s: %w", path, err)
	}
	return nil
}

// Update does a read-modify-write merge of kv into config.json.
func (s Store) Update(kv map[string]any) error {
	cfg, err := s.Load()
	if err != nil {
		return err
	}
	for k, v := range kv {
		cfg[k] = v
	}
	return s.Save(cfg)
}

func (s Store) markerPath(name string) string {
	return s.path(filepath.Join(StateDir, name+".done"))
}

// MarkDone touches the <name>.done marker file.
func (s Store) MarkDone(name string) error {
	dir := s.path(StateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(s.markerPath(name), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("config: mark %s done: %w", name, err)
	}
	return f.Close()
}

// IsDone reports whether the <name>.done marker exists.
func (s Store) IsDone(name string) bool {
	_, err := os.Stat(s.markerPath(name))
	return err == nil
}

// ErrNotDone is returned by RequireDone when a prerequisite stage marker
// is missing. Command dispatch (cmd/slfhst) treats this as a clean no-op
// exit (status 0), matching common.py's require_done_or_exit -- this
// package itself never calls os.Exit.
var ErrNotDone = fmt.Errorf("prerequisite stage not done")

// RequireDone returns ErrNotDone if the named stage marker is missing.
func (s Store) RequireDone(name string) error {
	if !s.IsDone(name) {
		return fmt.Errorf("%w: %q", ErrNotDone, name)
	}
	return nil
}

// GenSecret returns a URL-safe random token, the Go equivalent of
// Python's secrets.token_urlsafe(nbytes). Default of 24 raw bytes
// matches common.py's gen_secret() default.
func GenSecret(nbytes int) (string, error) {
	if nbytes <= 0 {
		nbytes = 24
	}
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("config: generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
