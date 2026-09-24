// Package installer is the live-ISO entrypoint (`slfhst install`).
package installer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/disks"
	"github.com/nnnithinnn/slfhst/internal/firewall"
	"github.com/nnnithinnn/slfhst/internal/netconf"
	"github.com/nnnithinnn/slfhst/internal/prompt"
	"github.com/nnnithinnn/slfhst/internal/sshauth"
	"github.com/nnnithinnn/slfhst/internal/users"
	"github.com/nnnithinnn/slfhst/internal/wizard"
)

const mountpoint = "/mnt/target"

// applianceDir is where the installer image embeds the built appliance
// content (Milestone C's job to put it there) -- fixed, simple names
// rather than mkosi's own versioned split-artifact filenames, so this
// package doesn't need to parse mkosi's naming convention.
const applianceDir = "/usr/share/slfhst/appliance"

func Run() error {
	prompt.Banner("slfhst installer")

	version, err := readApplianceVersion()
	if err != nil {
		return err
	}
	fmt.Printf("deploying appliance version %s\n", version)

	rootDisk, bulkDisk, err := disks.SelectDisks()
	if err != nil {
		return err
	}
	fmt.Printf("root disk: %s, bulk disk: %s\n", rootDisk, bulkDisk)

	if err := disks.PartitionRoot(rootDisk); err != nil {
		return err
	}

	usrRawPath := filepath.Join(applianceDir, "usr.raw")
	if err := disks.DeployUsrContent(rootDisk, usrRawPath, version); err != nil {
		return err
	}

	if err := disks.MountRootAndVar(rootDisk, mountpoint); err != nil {
		return err
	}
	if err := disks.MountUsr(rootDisk, mountpoint); err != nil {
		return err
	}
	if err := disks.EnsureFHSSymlinks(mountpoint); err != nil {
		return err
	}
	espPath, err := disks.MountESP(rootDisk, mountpoint)
	if err != nil {
		return err
	}

	efiSrcPath := filepath.Join(applianceDir, "uki.efi")
	if err := disks.DeployUKI(espPath, efiSrcPath, version); err != nil {
		return err
	}
	if err := disks.InstallBootloader(espPath); err != nil {
		return err
	}

	bulkPart, err := disks.PartitionBulk(bulkDisk)
	if err != nil {
		return err
	}
	if err := disks.MountBulk(mountpoint, bulkPart); err != nil {
		return err
	}

	store := config.Store{Root: mountpoint}

	if err := netconf.Configure(store); err != nil {
		return err
	}

	cfg, err := wizard.RunPreBoot(store)
	if err != nil {
		return err
	}

	if err := users.Create(mountpoint, cfg); err != nil {
		return err
	}

	if err := sshauth.Configure(mountpoint); err != nil {
		return err
	}

	if err := firewall.SetupPreBoot(mountpoint); err != nil {
		return err
	}

	// Materializes default service enablement (firewalld,
	// podman-auto-update.timer, our own slfhst-*.timer units, plus
	// Fedora's own defaults like sshd.service) into the target's
	// freshly-populated /etc -- see disks.ApplyPresets's doc comment for
	// why this has to be a preset applied here, not `systemctl enable`
	// baked into the image at build time.
	if err := disks.ApplyPresets(mountpoint); err != nil {
		return err
	}
	if err := disks.SetDefaultTarget(mountpoint); err != nil {
		return err
	}

	if err := store.MarkDone("stage1"); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("stage1 setup complete. Reboot to continue, then SSH in")
	fmt.Println("as the admin user and run `slfhst setup`.")
	return nil
}

func readApplianceVersion() (string, error) {
	path := filepath.Join(applianceDir, "VERSION")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("installer: read %s (embedded appliance version -- see Milestone C): %w", path, err)
	}
	version := string(data)
	for len(version) > 0 && (version[len(version)-1] == '\n' || version[len(version)-1] == '\r') {
		version = version[:len(version)-1]
	}
	if version == "" {
		return "", fmt.Errorf("installer: %s is empty", path)
	}
	return version, nil
}
