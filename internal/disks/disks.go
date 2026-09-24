// Package disks handles the installer's disk work: dynamic root+bulk
// disk selection (direct port of installer_disks.py's select_disks()),
// DPS partitioning of the root disk via systemd-repart (ESP + usr-A +
// usr-B + root + var -- confirmed working for real: a real repart.d
// definition set produced exactly this layout with correct DPS GPT
// types and the two usr slots correctly labeled `_empty`, which is what
// systemd-sysupdate looks for when picking a slot to write into), and
// plain parted+xfs for the bulk disk with a systemd `.mount` unit
// (per the user's explicit "no /etc/fstab" correction) instead of an
// fstab entry.
package disks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/runx"
)

// Provisional sizing -- deterministic beats clever (both usr slots are
// provisioned at install time, fixed size, rather than relying on
// systemd-repart's on-first-boot slot-growing trick), but the actual
// numbers here are placeholders pending a real appliance build size
// measurement (plan Phase 5). Generously sized on purpose: failing an
// install by running short is worse than a few hundred MB of slack on a
// VPS-sized disk.
const (
	espSize     = "512M"
	usrSlotSize = "3G"
	rootSize    = "1G"
)

// lsblkBool accepts lsblk -J's ROTA field in either shape this project
// has actually seen across util-linux versions: an older release quotes
// it as a "0"/"1" string, but Fedora 44's util-linux 2.41.5 (confirmed
// directly -- the same version the real appliance/installer ship)
// emits a raw JSON boolean instead. Same root cause and same fix
// pattern as blockDevice.Size below.
type lsblkBool bool

func (b *lsblkBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = s == "1" || s == "true"
	return nil
}

type blockDevice struct {
	Name   string      `json:"name"`
	Path   string      `json:"path"`
	Size   json.Number `json:"size"`
	Type   string      `json:"type"`
	Rota   lsblkBool   `json:"rota"`
	PKName string      `json:"pkname"`
}

type lsblkOutput struct {
	BlockDevices []blockDevice `json:"blockdevices"`
}

func listBlockDevices(columns string) ([]blockDevice, error) {
	out, err := runx.RunChecked([]string{"lsblk", "--list", "-J", "-b", "-o", columns}, runx.Options{})
	if err != nil {
		return nil, fmt.Errorf("disks: lsblk: %w", err)
	}
	var parsed lsblkOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("disks: parse lsblk output: %w", err)
	}
	return parsed.BlockDevices, nil
}

// sizeOf parses a blockDevice's SIZE field -- json.Number rather than a
// strconv call on a plain string field, for the same real reason as
// lsblkBool above: confirmed via an actual Fedora 44 container that
// `lsblk -J -b`'s SIZE field is a raw JSON number on this util-linux
// version, not the quoted string this code originally assumed
// unconditionally. json.Number accepts both shapes on unmarshal, so this
// is the fix, not a workaround -- a real, previously-unknown bug that
// would have broken `slfhst install`'s very first step (SelectDisks) on
// the real target, caught only by actually running it end to end
// against a real lsblk, not by unit tests against fixture data.
func sizeOf(d blockDevice) int64 {
	n, _ := d.Size.Int64()
	return n
}

// bootDiskLabel is this project's own installer ISO's volume label
// (build-iso.sh's VOLUME_LABEL) -- used to identify and exclude
// whatever device the installer actually booted from, no matter how
// it's attached. Confirmed a real, install-destroying bug by direct
// user report: the installer's own boot media is frequently reported
// by lsblk as TYPE="disk" (not "rom") when attached as virtual/USB
// media from an IPMI/BMC-style remote console rather than a true
// optical drive -- exactly the shape SelectDisks' own candidate filter
// was looking for. Since boot media is often the smallest device
// present, it was getting picked as the ROOT disk and would have been
// partitioned over, destroying the installer's own boot media mid-run.
const bootDiskLabel = "SLFHST_INSTALL"

// bootDiskName resolves the kernel device name (e.g. "sda", "sr0") of
// whatever disk carries this project's own known ISO volume label, so
// SelectDisks can exclude it -- works whether the label lands on a
// whole disk (a hybrid ISO attached/dd'd as one block device) or a
// partition (found via that partition's own PKName). Returns an error
// rather than silently skipping the exclusion if the boot device can't
// be positively identified -- given the consequences (partitioning
// over the installer's own boot media), failing loudly here is safer
// than proceeding without this protection.
func bootDiskName(all []blockDevice) (string, error) {
	out, err := runx.RunChecked([]string{"blkid", "-L", bootDiskLabel}, runx.Options{})
	if err != nil {
		return "", fmt.Errorf("disks: could not identify the installer's own boot device (blkid -L %s): %w -- refusing to select disks without being able to exclude it", bootDiskLabel, err)
	}
	bootName := filepath.Base(strings.TrimSpace(out))
	for _, d := range all {
		if d.Name != bootName {
			continue
		}
		if d.Type == "disk" {
			return d.Name, nil
		}
		return d.PKName, nil // a partition -- exclude its whole parent disk.
	}
	return "", fmt.Errorf("disks: boot device %s (label %s) not found in lsblk output", bootName, bootDiskLabel)
}

// SelectDisks picks the root disk (smallest non-rotational disk, else
// smallest overall) and the bulk disk (largest of what's left) --
// never the installer's own boot device, see bootDiskName above.
// Direct port of installer_disks.py's select_disks().
func SelectDisks() (rootDisk, bulkDisk string, err error) {
	all, err := listBlockDevices("NAME,PATH,SIZE,TYPE,ROTA,PKNAME")
	if err != nil {
		return "", "", err
	}
	excludeName, err := bootDiskName(all)
	if err != nil {
		return "", "", err
	}
	return selectDisksFrom(all, excludeName)
}

// selectDisksFrom is SelectDisks' pure selection logic, split out so
// tests can exercise it against fixed lsblk-shaped input instead of a
// real `lsblk`/`blkid` call. excludeName is the boot device's own
// lsblk NAME (see bootDiskName) -- never selected as root or bulk.
func selectDisksFrom(all []blockDevice, excludeName string) (rootDisk, bulkDisk string, err error) {
	var candidates []blockDevice
	for _, d := range all {
		if d.Type == "disk" && sizeOf(d) > 0 && (excludeName == "" || d.Name != excludeName) {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) < 2 {
		return "", "", fmt.Errorf("disks: need at least 2 disks (root + bulk), found %d", len(candidates))
	}

	var nonRotational []blockDevice
	for _, d := range candidates {
		if !bool(d.Rota) {
			nonRotational = append(nonRotational, d)
		}
	}
	pool := candidates
	if len(nonRotational) > 0 {
		pool = nonRotational
	}
	sort.Slice(pool, func(i, j int) bool { return sizeOf(pool[i]) < sizeOf(pool[j]) })
	rootDisk = pool[0].Path

	var others []blockDevice
	for _, d := range candidates {
		if d.Path != rootDisk {
			others = append(others, d)
		}
	}
	sort.Slice(others, func(i, j int) bool { return sizeOf(others[i]) > sizeOf(others[j]) })
	bulkDisk = others[0].Path

	return rootDisk, bulkDisk, nil
}

// repartDefinition renders one [Partition] unit for systemd-repart.
func repartDefinition(partType string, min, max, label, format string) string {
	var b strings.Builder
	b.WriteString("[Partition]\n")
	fmt.Fprintf(&b, "Type=%s\n", partType)
	if min != "" {
		fmt.Fprintf(&b, "SizeMinBytes=%s\n", min)
	}
	if max != "" {
		fmt.Fprintf(&b, "SizeMaxBytes=%s\n", max)
	}
	if format != "" {
		fmt.Fprintf(&b, "Format=%s\n", format)
	}
	if label != "" {
		fmt.Fprintf(&b, "Label=%s\n", label)
	}
	return b.String()
}

// PartitionRoot lays out the root disk per the DPS scheme (ESP + usr-A +
// usr-B + persistent root + persistent var) via systemd-repart -- not
// hand-typed GPT type GUIDs, which are architecture-specific and easy to
// get subtly wrong; systemd-repart already knows the right ones for
// whatever architecture it's running on, confirmed by a real run
// producing correct x86-64/arm64-appropriate types. root and var get
// fixed labels ("slfhst-root"/"slfhst-var") so they can be found
// afterward without depending on GPT type GUIDs either.
func PartitionRoot(disk string) error {
	dir, err := os.MkdirTemp("", "slfhst-repart-")
	if err != nil {
		return fmt.Errorf("disks: create repart definitions dir: %w", err)
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"10-esp.conf":   repartDefinition("esp", espSize, espSize, "", "vfat"),
		"20-usr-a.conf": repartDefinition("usr", usrSlotSize, usrSlotSize, "_empty", "erofs"),
		"21-usr-b.conf": repartDefinition("usr", usrSlotSize, usrSlotSize, "_empty", "erofs"),
		"30-root.conf":  repartDefinition("root", rootSize, rootSize, "slfhst-root", "ext4"),
		"40-var.conf":   repartDefinition("var", "256M", "", "slfhst-var", "ext4"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("disks: write %s: %w", name, err)
		}
	}

	_, err = runx.Run([]string{"systemd-repart", "--definitions=" + dir, "--dry-run=no", "--empty=allow", disk},
		runx.Options{Capture: true})
	if err != nil {
		return fmt.Errorf("disks: systemd-repart %s: %w", disk, err)
	}
	return nil
}

// findPartitionByLabel returns the /dev path of disk's child partition
// with the given GPT PARTLABEL. Queried directly as a generic map
// (rather than adding a rarely-used PartLabel field to blockDevice)
// since this is the only place that needs it.
func findPartitionByLabel(disk, label string) (string, error) {
	out, err := runx.RunChecked([]string{"lsblk", "--list", "-J", "-b", "-o", "NAME,PATH,PKNAME,PARTLABEL"}, runx.Options{})
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
		partlabel, _ := d["partlabel"].(string)
		path, _ := d["path"].(string)
		if pkname == diskName && partlabel == label {
			return path, nil
		}
	}
	return "", fmt.Errorf("disks: no partition on %s labeled %q", disk, label)
}

// MountRootAndVar mounts the root and var partitions (found by the
// fixed labels PartitionRoot gave them) at mountpoint and
// mountpoint/var.
func MountRootAndVar(disk, mountpoint string) error {
	rootPart, err := findPartitionByLabel(disk, "slfhst-root")
	if err != nil {
		return err
	}
	varPart, err := findPartitionByLabel(disk, "slfhst-var")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", mountpoint, err)
	}
	if _, err := runx.Run([]string{"mount", rootPart, mountpoint}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s: %w", rootPart, err)
	}
	varMount := filepath.Join(mountpoint, "var")
	if err := os.MkdirAll(varMount, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", varMount, err)
	}
	if _, err := runx.Run([]string{"mount", varPart, varMount}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s: %w", varPart, err)
	}
	return nil
}

// EnsureFHSSymlinks creates the standard usr-merge compatibility
// symlinks (bin, sbin, lib, lib64 -> usr/...) at the root of the target
// tree -- found to be genuinely necessary, not just tidy, by actually
// running the installer end to end: the persistent root partition is
// freshly formatted ext4 with nothing on it but what this project's own
// code writes, and nowhere in this DPS design does anything else create
// these symlinks (a traditional single-partition install gets them for
// free from a base "filesystem" package; this project's split
// root/usr layout has no equivalent). Without them, any ELF binary
// whose dynamic linker is referenced via an absolute /lib(64)/... path
// (effectively all of them) fails to execute inside a chroot of the
// target -- confirmed directly: `chroot <target> sshd -t` failed with
// "No such file or directory" for a real, present sshd binary, because
// /lib/ld-linux-*.so.* couldn't be found with no /lib symlink at the
// new root. This would fail identically on the real deployed
// appliance's first actual boot, not just here -- nothing else in the
// chain would have caught it before a real boot attempt. Call after
// `usr` is mounted (MountUsr), so the usr/<name> existence check below
// reflects the real content, not an empty mountpoint.
func EnsureFHSSymlinks(mountpoint string) error {
	links := map[string]string{
		"bin":   "usr/bin",
		"sbin":  "usr/sbin",
		"lib":   "usr/lib",
		"lib64": "usr/lib64",
	}
	for name, target := range links {
		if _, err := os.Lstat(filepath.Join(mountpoint, target)); err != nil {
			continue // this arch/build doesn't have a usr/<name> to link to (e.g. no lib64 on some architectures).
		}
		path := filepath.Join(mountpoint, name)
		if _, err := os.Lstat(path); err == nil {
			continue // already exists -- idempotent, matches other installer steps.
		}
		if err := os.Symlink(target, path); err != nil {
			return fmt.Errorf("disks: symlink %s -> %s: %w", path, target, err)
		}
	}
	return nil
}

// PartitionBulk lays out the bulk data disk as a single GPT+xfs
// partition -- unrelated to the DPS scheme above (a second physical
// disk), direct port of disks.py's partition_and_format().
func PartitionBulk(disk string) (partition string, err error) {
	_, err = runx.Run([]string{"parted", "--script", disk,
		"mklabel", "gpt", "mkpart", "primary", "xfs", "0%", "100%"}, runx.Options{})
	if err != nil {
		return "", fmt.Errorf("disks: partition %s: %w", disk, err)
	}
	if _, err := runx.Run([]string{"partprobe", disk}, runx.Options{}); err != nil {
		return "", fmt.Errorf("disks: partprobe %s: %w", disk, err)
	}
	if _, err := runx.Run([]string{"udevadm", "settle"}, runx.Options{}); err != nil {
		return "", fmt.Errorf("disks: udevadm settle: %w", err)
	}

	all, err := listBlockDevices("NAME,PATH,PKNAME")
	if err != nil {
		return "", err
	}
	diskName := filepath.Base(disk)
	for _, d := range all {
		if d.PKName == diskName {
			partition = d.Path
			break
		}
	}
	if partition == "" {
		return "", fmt.Errorf("disks: no partition found on %s after partitioning", disk)
	}

	if _, err := runx.Run([]string{"mkfs.xfs", "-f", partition}, runx.Options{Capture: true}); err != nil {
		return "", fmt.Errorf("disks: mkfs.xfs %s: %w", partition, err)
	}
	return partition, nil
}

var dataSubdirs = []string{"stalwart", "vaultwarden", "garage", "traefik", "ente"}

// MountBulk mounts the bulk partition at <root>/srv/data and creates the
// per-service subdirectories, then writes and enables a systemd
// `.mount` unit (not an /etc/fstab entry, per the user's explicit
// correction) so it mounts automatically on every subsequent real boot.
func MountBulk(root, partition string) error {
	target := filepath.Join(root, "srv/data")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", target, err)
	}
	if _, err := runx.Run([]string{"mount", partition, target}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s at %s: %w", partition, target, err)
	}
	for _, name := range dataSubdirs {
		if err := os.MkdirAll(filepath.Join(target, name), 0o755); err != nil {
			return fmt.Errorf("disks: mkdir %s/%s: %w", target, name, err)
		}
	}
	return writeBulkMountUnit(root, partition)
}

func writeBulkMountUnit(root, partition string) error {
	uuid, err := runx.RunChecked([]string{"blkid", "-s", "UUID", "-o", "value", partition}, runx.Options{})
	if err != nil {
		return fmt.Errorf("disks: blkid %s: %w", partition, err)
	}
	uuid = strings.TrimSpace(uuid)

	content := fmt.Sprintf(`[Unit]
Description=slfhst bulk data

[Mount]
What=/dev/disk/by-uuid/%s
Where=/srv/data
Type=xfs

[Install]
WantedBy=local-fs.target
`, uuid)

	unitPath := filepath.Join(root, "etc/systemd/system/srv-data.mount")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("disks: mkdir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("disks: write %s: %w", unitPath, err)
	}

	wantsDir := filepath.Join(root, "etc/systemd/system/local-fs.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", wantsDir, err)
	}
	linkPath := filepath.Join(wantsDir, "srv-data.mount")
	_ = os.Remove(linkPath) // idempotent: ignore "doesn't exist yet".
	if err := os.Symlink("/etc/systemd/system/srv-data.mount", linkPath); err != nil {
		return fmt.Errorf("disks: symlink %s: %w", linkPath, err)
	}
	return nil
}
