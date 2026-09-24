// Deploying the built appliance content onto the partitioned root disk
// and installing the bootloader -- plan "Next stage" Milestone B.
//
// Confirmed via DeepWiki against systemd/systemd before writing this:
// systemd-sysupdate does NOT do initial partition population -- it
// expects the target partition to already exist and be labeled `_empty`,
// and only takes over from the first real `slfhst update` onward. Direct
// `dd` + relabeling the partition to match the label convention a later
// `.transfer` definition's Target MatchPattern= will look for is "the
// normal way installers populate the first slot," per that same
// research -- not a shortcut, the documented pattern.
package disks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nnnithinnn/slfhst/internal/runx"
)

// Deterministic by construction: PartitionRoot's repart.d files are
// numbered 10-esp/20-usr-a/21-usr-b/30-root/40-var, so systemd-repart
// creates them in exactly this partition-number order every time.
const (
	espPartNum  = 1
	usrAPartNum = 2
	usrBPartNum = 3
	rootPartNum = 4
	varPartNum  = 5
)

// findPartitionByNumber returns the /dev path of disk's Nth partition.
func findPartitionByNumber(disk string, n int) (string, error) {
	out, err := runx.RunChecked([]string{"lsblk", "--list", "-J", "-b", "-o", "NAME,PATH,PKNAME,PARTN"}, runx.Options{})
	if err != nil {
		return "", fmt.Errorf("disks: lsblk: %w", err)
	}
	var raw struct {
		BlockDevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return "", fmt.Errorf("disks: parse lsblk output: %w", err)
	}
	diskName := filepath.Base(disk)
	for _, d := range raw.BlockDevices {
		pkname, _ := d["pkname"].(string)
		path, _ := d["path"].(string)
		partn, ok := d["partn"].(float64) // JSON numbers decode as float64
		if pkname == diskName && ok && int(partn) == n {
			return path, nil
		}
	}
	return "", fmt.Errorf("disks: no partition %d found on %s", n, disk)
}

// DeployUsrContent writes the built usr-<arch>.raw split artifact
// (already a complete erofs filesystem image, confirmed this session --
// no mount+copy needed) directly onto the usr-A partition via a raw
// block copy, then relabels that partition from `_empty` to
// "slfhst_<version>" -- the label a `slfhst_@v`-patterned
// systemd-sysupdate Target MatchPattern= will recognize as "this version
// is currently installed" on the first real update.
func DeployUsrContent(disk, usrRawPath, version string) error {
	part, err := findPartitionByNumber(disk, usrAPartNum)
	if err != nil {
		return err
	}
	if err := ddCopy(usrRawPath, part); err != nil {
		return fmt.Errorf("disks: write %s onto %s: %w", usrRawPath, part, err)
	}
	label := "slfhst_" + version
	if _, err := runx.Run([]string{"parted", "--script", disk, "name", fmt.Sprint(usrAPartNum), label},
		runx.Options{}); err != nil {
		return fmt.Errorf("disks: relabel partition %d as %q: %w", usrAPartNum, label, err)
	}
	return nil
}

// ddCopy does a plain block copy of src onto dst -- os/exec around `dd`
// rather than reimplementing block-aligned I/O in Go for no benefit.
func ddCopy(src, dst string) error {
	_, err := runx.Run([]string{"dd", "if=" + src, "of=" + dst, "bs=4M", "conv=fsync"},
		runx.Options{Capture: true})
	return err
}

// MountUsr mounts the usr-A partition (read-only -- it's erofs, and
// nothing should be writing to it anyway) at mountpoint/usr. Needed
// pre-boot for two things: totp.ValidateSSHDConfig's `chroot ... sshd -t`
// (confirmed this session that it needs a real /usr/sbin/sshd inside the
// chroot) and `systemctl preset-all --root=<mountpoint>` (see
// InstallBootloader's sibling, ApplyPresets, below).
func MountUsr(disk, mountpoint string) error {
	part, err := findPartitionByNumber(disk, usrAPartNum)
	if err != nil {
		return err
	}
	usrMount := filepath.Join(mountpoint, "usr")
	if err := os.MkdirAll(usrMount, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", usrMount, err)
	}
	if _, err := runx.Run([]string{"mount", "-o", "ro", part, usrMount}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s: %w", part, err)
	}
	return nil
}

// MountESP mounts the ESP partition at mountpoint/efi.
//
// NOT VERIFIED as the right mountpoint convention -- plan's Milestone B
// flags this explicitly: this project's DPS layout has no separate
// XBOOTLDR partition (unlike the combined boot+ESP+XBOOTLDR setup this
// session's mkosi smoke build showed), so whether ESP-only with the UKI
// placed directly under EFI/Linux/ is correct needs confirming with a
// real `bootctl` pass, not assumed from the mountpoint name alone.
func MountESP(disk, mountpoint string) (espPath string, err error) {
	part, err := findPartitionByNumber(disk, espPartNum)
	if err != nil {
		return "", err
	}
	espMount := filepath.Join(mountpoint, "efi")
	if err := os.MkdirAll(espMount, 0o755); err != nil {
		return "", fmt.Errorf("disks: mkdir %s: %w", espMount, err)
	}
	if _, err := runx.Run([]string{"mount", part, espMount}, runx.Options{}); err != nil {
		return "", fmt.Errorf("disks: mount ESP %s: %w", part, err)
	}
	return espMount, nil
}

// DeployUKI copies the built UKI (.efi) artifact into the ESP's
// EFI/Linux/ directory, the path mkosi's own build already uses
// internally for the same file (confirmed via this session's build
// output: "Wrote unsigned .../boot/EFI/Linux/<name>.efi").
func DeployUKI(espPath, efiSrcPath, version string) error {
	destDir := filepath.Join(espPath, "EFI", "Linux")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", destDir, err)
	}
	dest := filepath.Join(destDir, "slfhst_"+version+".efi")
	if err := copyFile(efiSrcPath, dest); err != nil {
		return fmt.Errorf("disks: copy %s to %s: %w", efiSrcPath, dest, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// InstallBootloader runs `bootctl install` against the mounted ESP.
//
// NOT VERIFIED against a real UEFI-capable target -- this sandbox has no
// UEFI firmware at all (confirmed: no /sys/firmware/efi anywhere in this
// environment), so bootctl's exact offline-install behavior for a
// not-yet-booted target is unconfirmed. bootctl does document real
// support for installing without being booted via live UEFI firmware;
// confirm the right invocation against a real VM/hardware before trusting
// this blindly.
func InstallBootloader(espPath string) error {
	if _, err := runx.Run([]string{"bootctl", "--esp-path=" + espPath, "install"},
		runx.Options{Capture: true}); err != nil {
		return fmt.Errorf("disks: bootctl install: %w", err)
	}
	return nil
}

// ApplyPresets runs `systemctl preset-all --root=<mountpoint>`,
// materializing the default-enabled-service symlinks (ours:
// firewalld.service, podman-auto-update.timer, slfhst-monitor.timer,
// slfhst-update.timer, via mkosi/appliance/mkosi.extra/usr/lib/systemd/
// system-preset/80-slfhst.preset -- plus Fedora's own defaults, e.g.
// sshd.service) into the target's freshly-populated /etc. Confirmed this
// session against real built content: a plain `systemctl enable` at
// image-build time produces no symlinks anywhere reachable (they'd land
// under /etc, which isn't part of the swappable usr content at all) --
// preset-all at install time, once /etc actually exists, is the correct
// systemd-native mechanism, verified to produce the right symlinks
// (including exactly slfhst's four units) against the real usr partition
// built this session.
func ApplyPresets(mountpoint string) error {
	if _, err := runx.Run([]string{"systemctl", "preset-all", "--root=" + mountpoint},
		runx.Options{Capture: true}); err != nil {
		return fmt.Errorf("disks: systemctl preset-all: %w", err)
	}
	return nil
}
