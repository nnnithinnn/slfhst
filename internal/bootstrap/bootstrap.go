// Package bootstrap orchestrates stage2 -- render quadlets, pull+start
// containers, bootstrap Garage/Postgres/Stalwart, sync Cloudflare DNS,
// write the MOTD. Admin-run over SSH once stage1 (now part of the
// installer) has completed. Direct port of cli.py's cmd_bootstrap.
package bootstrap

import (
	"github.com/nnnithinnn/slfhst/internal/backing"
	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/deploy"
	"github.com/nnnithinnn/slfhst/internal/dnscf"
	"github.com/nnnithinnn/slfhst/internal/healthcheck"
	"github.com/nnnithinnn/slfhst/internal/quadlet"
)

// Run executes stage2 in order, matching cli.py's cmd_bootstrap:
// quadlets -> deploy -> backing -> dnscf -> healthcheck -> mark stage2
// done. Each step already guards on stage1 being done itself (same
// require_done_or_exit convention as the Python), and each is
// independently idempotent, so re-running Run after a partial failure
// is safe.
func Run(store config.Store) error {
	if err := store.RequireDone("stage1"); err != nil {
		return err
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}

	if err := quadlet.EnsureSecrets(cfg); err != nil {
		return err
	}
	if err := quadlet.RenderAll(cfg); err != nil {
		return err
	}
	if err := deploy.Run(store); err != nil {
		return err
	}
	if err := backing.Run(store); err != nil {
		return err
	}
	if err := dnscf.Sync(store); err != nil {
		return err
	}
	if err := healthcheck.Run(store); err != nil {
		return err
	}
	return store.MarkDone("stage2")
}
