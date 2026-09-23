// Package backing does the one-time-idempotent bootstrap of backing
// services after first container start: Garage single-node layout/
// bucket/key, Postgres readiness wait, Stalwart hardening via
// stalwart-cli. Direct port of backing.py.
package backing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/runx"
)

const (
	garageBucket  = "ente"
	garageKeyName = "museum"

	stalwartCLIImage = "docker.io/stalwartlabs/cli:1.0.12"
)

var stalwartPlanDir = filepath.Join(config.DataDir, "stalwart", "slfhst-plan")

func podmanExecGarage(container string, args ...string) (string, error) {
	argv := append([]string{"podman", "exec", container, "garage"}, args...)
	return runx.RunChecked(argv, runx.Options{AsUser: config.ServiceUser})
}

// waitFor polls `podman exec <container> true` until it succeeds or
// timeout elapses.
func waitFor(container string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := runx.Run([]string{"podman", "exec", container, "true"},
			runx.Options{AsUser: config.ServiceUser, Capture: true, NoCheck: true})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("backing: %s did not become ready within %s", container, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

// garageCapacity picks a layout capacity for the single garage node:
// free space under DataDir/garage, minus a 5G headroom, floored at 5G.
func garageCapacity() (string, error) {
	return garageCapacityAt(filepath.Join(config.DataDir, "garage"))
}

func garageCapacityAt(dir string) (string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return "", fmt.Errorf("backing: statfs %s: %w", dir, err)
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	const gb = 1024 * 1024 * 1024
	freeGB := int64(freeBytes/gb) - 5
	if freeGB < 5 {
		freeGB = 5
	}
	return fmt.Sprintf("%dG", freeGB), nil
}

// BootstrapGarage waits for the garage container, then idempotently
// assigns a single-node layout and ensures the "ente" bucket + "museum"
// key (read/write/owner) exist. Direct port of backing.py's
// bootstrap_garage().
func BootstrapGarage() error {
	if err := waitFor("garage", 120*time.Second); err != nil {
		return err
	}

	layout, err := podmanExecGarage("garage", "layout", "show")
	if err != nil {
		return fmt.Errorf("backing: garage layout show: %w", err)
	}
	alreadyAssigned := !strings.Contains(layout, "NO ROLE ASSIGNED") &&
		strings.Contains(layout, "==== HEALTHY NODES ====") &&
		strings.Contains(strings.ToLower(layout), "capacity")

	if !alreadyAssigned {
		nodeID, err := podmanExecGarage("garage", "node", "id", "-q")
		if err != nil {
			return fmt.Errorf("backing: garage node id: %w", err)
		}
		nodeID = strings.TrimSpace(nodeID)

		capacity, err := garageCapacity()
		if err != nil {
			return err
		}
		if _, err := podmanExecGarage("garage", "layout", "assign", "-z", "dc1", "-c", capacity, nodeID); err != nil {
			return fmt.Errorf("backing: garage layout assign: %w", err)
		}
		if _, err := podmanExecGarage("garage", "layout", "apply", "--version", "1"); err != nil {
			return fmt.Errorf("backing: garage layout apply: %w", err)
		}
	}

	buckets, err := podmanExecGarage("garage", "bucket", "list")
	if err != nil {
		return fmt.Errorf("backing: garage bucket list: %w", err)
	}
	if !strings.Contains(buckets, garageBucket) {
		if _, err := podmanExecGarage("garage", "bucket", "create", garageBucket); err != nil {
			return fmt.Errorf("backing: garage bucket create: %w", err)
		}
	}

	keys, err := podmanExecGarage("garage", "key", "list")
	if err != nil {
		return fmt.Errorf("backing: garage key list: %w", err)
	}
	if !strings.Contains(keys, garageKeyName) {
		if _, err := podmanExecGarage("garage", "key", "create", garageKeyName); err != nil {
			return fmt.Errorf("backing: garage key create: %w", err)
		}
		if _, err := podmanExecGarage("garage", "bucket", "allow", "--read", "--write", "--owner",
			garageBucket, "--key", garageKeyName); err != nil {
			return fmt.Errorf("backing: garage bucket allow: %w", err)
		}
	}
	return nil
}

// WaitForPostgres waits for the postgres container to become reachable.
func WaitForPostgres() error {
	return waitFor("postgres", 120*time.Second)
}

// stalwartHardeningPlan builds the NDJSON-ready ops list for
// `stalwart-cli apply` -- Stalwart v0.16 has no config.toml, everything
// is a JMAP object set declaratively. Direct port of backing.py's
// _stalwart_hardening_plan(). Field/object names verified against
// Stalwart's schema source and stalwartlabs/cli's apply.rs RawOp enum in
// the original Python pass -- never run against a live v0.16 instance in
// this Go port either, same caveat carried forward.
func stalwartHardeningPlan(mailHost string) []map[string]any {
	return []map[string]any{
		{
			"@type": "update", "object": "SystemSettings", "id": "singleton",
			"value": map[string]any{"defaultHostname": mailHost},
		},
		{
			"@type": "update", "object": "Security", "id": "singleton",
			"value": map[string]any{
				"authBanRate":    map[string]any{"count": 5, "period": 120000},
				"authBanPeriod":  3600000,
				"abuseBanRate":   map[string]any{"count": 3, "period": 60000},
				"abuseBanPeriod": 3600000,
			},
		},
		{
			"@type": "upsert", "object": "MtaInboundThrottle", "matchOn": []string{"description"},
			"value": map[string]any{
				"slfhst_ip_burst": map[string]any{
					"enable": true, "description": "slfhst: remote IP burst",
					"key":   []string{"remoteIp"},
					"match": map[string]any{"match": []any{}, "else": "true"},
					"rate":  map[string]any{"count": 20, "period": 60000},
				},
				"slfhst_ip_sustained": map[string]any{
					"enable": true, "description": "slfhst: remote IP sustained",
					"key":   []string{"remoteIp"},
					"match": map[string]any{"match": []any{}, "else": "true"},
					"rate":  map[string]any{"count": 300, "period": 3600000},
				},
			},
		},
	}
}

// HardenStalwart waits for the stalwart container, then applies the
// hardening plan via a one-shot stalwart-cli container run, retrying for
// up to 2 minutes. A no-op with a warning if stalwart_admin_password
// isn't configured. Direct port of backing.py's harden_stalwart().
func HardenStalwart(cfg map[string]any) error {
	if err := waitFor("stalwart", 120*time.Second); err != nil {
		return err
	}

	password, _ := cfg["stalwart_admin_password"].(string)
	if password == "" {
		fmt.Println("warning: backing: no stalwart_admin_password in config, skipping hardening")
		return nil
	}
	domain, _ := cfg["domain"].(string)
	mailHost := "mail." + domain

	var buf strings.Builder
	for _, op := range stalwartHardeningPlan(mailHost) {
		data, err := json.Marshal(op)
		if err != nil {
			return fmt.Errorf("backing: marshal hardening plan op: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	if err := os.MkdirAll(stalwartPlanDir, 0o755); err != nil {
		return fmt.Errorf("backing: mkdir %s: %w", stalwartPlanDir, err)
	}
	planPath := filepath.Join(stalwartPlanDir, "plan.ndjson")
	if err := os.WriteFile(planPath, []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("backing: write %s: %w", planPath, err)
	}

	deadline := time.Now().Add(2 * time.Minute)
	argv := []string{
		"podman", "run", "--rm", "--network", "slfhst",
		"-v", stalwartPlanDir + ":/work:Z", "-w", "/work",
		"-e", "STALWART_URL=http://stalwart:8080",
		"-e", "STALWART_USER=admin",
		"-e", "STALWART_PASSWORD=" + password,
		stalwartCLIImage, "apply", "--file", "plan.ndjson",
	}
	for {
		res, err := runx.Run(argv, runx.Options{AsUser: config.ServiceUser, Capture: true, NoCheck: true})
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Printf("warning: backing: stalwart-cli apply did not succeed within 2m, giving up (last stderr: %s)\n", res.Stderr)
			return nil
		}
		time.Sleep(5 * time.Second)
	}
}

// Run is stage2's backing-service bootstrap step: Postgres readiness,
// Garage layout/bucket/key, Stalwart hardening. Direct port of
// backing.py's main().
func Run(store config.Store) error {
	if err := store.RequireDone("stage1"); err != nil {
		return err
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}
	if err := WaitForPostgres(); err != nil {
		return err
	}
	if err := BootstrapGarage(); err != nil {
		return err
	}
	return HardenStalwart(cfg)
}
