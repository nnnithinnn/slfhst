package config

import (
	"errors"
	"os"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Root: t.TempDir()}

	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("Load on fresh root: %v", err)
	}
	if len(cfg) != 0 {
		t.Fatalf("Load on fresh root: want empty map, got %v", cfg)
	}

	if err := store.Update(map[string]any{"hostname": "box1", "domain": "example.com"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := store.Update(map[string]any{"admin_user": "admin"}); err != nil {
		t.Fatalf("second Update: %v", err)
	}

	cfg, err = store.Load()
	if err != nil {
		t.Fatalf("Load after Update: %v", err)
	}
	want := map[string]any{"hostname": "box1", "domain": "example.com", "admin_user": "admin"}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("cfg[%q] = %v, want %v", k, cfg[k], v)
		}
	}

	info, err := os.Stat(store.path(ConfigFile))
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perm = %o, want 0600", perm)
	}
}

func TestMarkerLifecycle(t *testing.T) {
	store := Store{Root: t.TempDir()}

	if store.IsDone("stage1") {
		t.Fatal("IsDone true before MarkDone")
	}
	if err := store.RequireDone("stage1"); !errors.Is(err, ErrNotDone) {
		t.Fatalf("RequireDone before MarkDone: got %v, want ErrNotDone", err)
	}

	if err := store.MarkDone("stage1"); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if !store.IsDone("stage1") {
		t.Fatal("IsDone false after MarkDone")
	}
	if err := store.RequireDone("stage1"); err != nil {
		t.Fatalf("RequireDone after MarkDone: %v", err)
	}
}

func TestGenSecret(t *testing.T) {
	a, err := GenSecret(0)
	if err != nil {
		t.Fatalf("GenSecret: %v", err)
	}
	b, err := GenSecret(0)
	if err != nil {
		t.Fatalf("GenSecret: %v", err)
	}
	if a == b {
		t.Fatal("GenSecret returned the same value twice")
	}
	if len(a) < 20 {
		t.Fatalf("GenSecret result looks too short: %q", a)
	}
}
