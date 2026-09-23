package quadlet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictExpand(t *testing.T) {
	vars := map[string]string{"DOMAIN": "example.com", "MAIL_HOST": "mail.example.com"}

	got, err := strictExpand("https://${MAIL_HOST} on $DOMAIN", vars)
	if err != nil {
		t.Fatalf("strictExpand: %v", err)
	}
	want := "https://mail.example.com on example.com"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	_, err = strictExpand("${UNDEFINED_VAR}", vars)
	if err == nil {
		t.Fatal("expected an error for an undefined variable, got nil")
	}
}

func TestResolveDest(t *testing.T) {
	cases := []struct {
		stem       string
		wantSuffix string
		wantOK     bool
	}{
		{"traefik-dynamic.yml", "traefik/dynamic/traefik-dynamic.yml", true},
		{"museum.yaml", "ente/museum.yaml", true},
		{"garage.toml", "garage/garage.toml", true},
		{"stalwart.container", ".config/containers/systemd/stalwart.container", true},
		{"slfhst-network.network", ".config/containers/systemd/slfhst-network.network", true},
		{"postgres-data.volume", ".config/containers/systemd/postgres-data.volume", true},
		{"slfhst.target", ".config/systemd/user/slfhst.target", true},
		{"unknown.txt", "", false},
	}
	for _, c := range cases {
		dest, ok := resolveDest(c.stem)
		if ok != c.wantOK {
			t.Errorf("resolveDest(%q) ok = %v, want %v", c.stem, ok, c.wantOK)
			continue
		}
		if ok && !strings.HasSuffix(dest, c.wantSuffix) {
			t.Errorf("resolveDest(%q) = %q, want suffix %q", c.stem, dest, c.wantSuffix)
		}
	}
}

// TestRenderRealTemplates exercises strictExpand against every real
// .tmpl file shipped in usr/share/slfhst/templates, with a config.json
// shaped like a real wizard run would produce -- catches template/var
// drift directly (e.g. a template referencing a variable templateVars
// doesn't provide) without needing a live system.
func TestRenderRealTemplates(t *testing.T) {
	repoRoot := findRepoRoot(t)
	templatesDir := filepath.Join(repoRoot, "usr", "share", "slfhst", "templates")

	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		t.Fatalf("read %s: %v", templatesDir, err)
	}

	cfg := map[string]any{
		"domain":                   "example.com",
		"alert_email":              "admin@example.com",
		"postgres_password":        "pw",
		"garage_admin_token":       "tok",
		"garage_rpc_secret":        "secret",
		"ente_encryption_key":      "key",
		"ente_encryption_hash_key": "hashkey",
		"ente_jwt_secret":          "jwt",
	}
	vars, err := templateVars(cfg)
	if err != nil {
		t.Fatalf("templateVars: %v", err)
	}

	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmpl") {
			continue
		}
		found++
		path := filepath.Join(templatesDir, e.Name())
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := strictExpand(string(text), vars); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
		}
		stem := strings.TrimSuffix(e.Name(), ".tmpl")
		if _, ok := resolveDest(stem); !ok {
			t.Errorf("%s: no destination rule for stem %q", e.Name(), stem)
		}
	}
	if found == 0 {
		t.Fatal("no .tmpl files found -- test fixture path is wrong")
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod) above " + dir)
		}
		dir = parent
	}
}
