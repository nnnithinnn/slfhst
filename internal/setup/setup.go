package setup

import (
	"fmt"

	"github.com/nnnithinnn/slfhst/internal/backing"
	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/deploy"
	"github.com/nnnithinnn/slfhst/internal/dnscf"
	"github.com/nnnithinnn/slfhst/internal/firewall"
	"github.com/nnnithinnn/slfhst/internal/healthcheck"
	"github.com/nnnithinnn/slfhst/internal/quadlet"
	"github.com/nnnithinnn/slfhst/internal/sysupdate"
	"github.com/nnnithinnn/slfhst/internal/wizard"
)

func Run(store config.Store) error {
	if err := store.RequireDone("stage1"); err != nil {
		return err
	}

	cfg, err := wizard.RunPostBoot(store)
	if err != nil {
		return err
	}

	if err := firewall.SyncCloudflareIPSets(); err != nil {
		return err
	}

	if err := sysupdate.Check(); err != nil {
		fmt.Println("update check failed:", err)
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
